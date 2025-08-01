package containerd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/containerd/containerd/v2/core/content"
	c8dimages "github.com/containerd/containerd/v2/core/images"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/containerd/log"
	"github.com/containerd/platforms"
	"github.com/distribution/reference"
	"github.com/google/uuid"
	imagespec "github.com/moby/docker-image-spec/specs-go/v1"
	"github.com/moby/go-archive/compression"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/events"
	"github.com/moby/moby/v2/daemon/builder/dockerfile"
	"github.com/moby/moby/v2/daemon/internal/image"
	"github.com/moby/moby/v2/errdefs"
	"github.com/moby/moby/v2/pkg/pools"
	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
)

// ImportImage imports an image, getting the archived layer data from layerReader.
// Layer archive is imported as-is if the compression is gzip or zstd.
// Uncompressed, xz and bzip2 archives are recompressed into gzip.
// The image is tagged with the given reference.
// If the platform is nil, the default host platform is used.
// The message is used as the history comment.
// Image configuration is derived from the dockerfile instructions in changes.
func (i *ImageService) ImportImage(ctx context.Context, ref reference.Named, platform *ocispec.Platform, msg string, layerReader io.Reader, changes []string) (image.ID, error) {
	refString := ""
	if ref != nil {
		refString = ref.String()
	}
	logger := log.G(ctx).WithField("ref", refString)

	ctx, release, err := i.client.WithLease(ctx)
	if err != nil {
		return "", errdefs.System(err)
	}
	defer func() {
		if err := release(context.WithoutCancel(ctx)); err != nil {
			logger.WithError(err).Warn("failed to release lease created for import")
		}
	}()

	if platform == nil {
		def := platforms.DefaultSpec()
		platform = &def
	}

	imageConfig, err := dockerfile.BuildFromConfig(ctx, &container.Config{}, changes, platform.OS)
	if err != nil {
		logger.WithError(err).Debug("failed to process changes")
		return "", errdefs.InvalidParameter(err)
	}

	cs := i.content

	compressedDigest, uncompressedDigest, mt, err := saveArchive(ctx, cs, layerReader)
	if err != nil {
		logger.WithError(err).Debug("failed to write layer blob")
		return "", err
	}
	logger = logger.WithFields(log.Fields{
		"compressedDigest":   compressedDigest,
		"uncompressedDigest": uncompressedDigest,
	})

	size, err := fillUncompressedLabel(ctx, cs, compressedDigest, uncompressedDigest)
	if err != nil {
		logger.WithError(err).Debug("failed to set uncompressed label on the compressed blob")
		return "", err
	}

	compressedRootfsDesc := ocispec.Descriptor{
		MediaType: mt,
		Digest:    compressedDigest,
		Size:      size,
	}

	dockerCfg := containerConfigToDockerOCIImageConfig(imageConfig)
	createdAt := time.Now()
	config := imagespec.DockerOCIImage{
		Image: ocispec.Image{
			Platform: *platform,
			Created:  &createdAt,
			Author:   "",
			RootFS: ocispec.RootFS{
				Type:    "layers",
				DiffIDs: []digest.Digest{uncompressedDigest},
			},
			History: []ocispec.History{
				{
					Created:    &createdAt,
					CreatedBy:  "",
					Author:     "",
					Comment:    msg,
					EmptyLayer: false,
				},
			},
		},
		Config: dockerCfg,
	}
	configDesc, err := storeJson(ctx, cs, ocispec.MediaTypeImageConfig, config, nil)
	if err != nil {
		return "", err
	}

	manifest := ocispec.Manifest{
		MediaType: ocispec.MediaTypeImageManifest,
		Versioned: specs.Versioned{
			SchemaVersion: 2,
		},
		Config: configDesc,
		Layers: []ocispec.Descriptor{
			compressedRootfsDesc,
		},
	}
	manifestDesc, err := storeJson(ctx, cs, ocispec.MediaTypeImageManifest, manifest, map[string]string{
		"containerd.io/gc.ref.content.config": configDesc.Digest.String(),
		"containerd.io/gc.ref.content.l.0":    compressedDigest.String(),
	})
	if err != nil {
		return "", err
	}

	id := image.ID(manifestDesc.Digest.String())
	img := c8dimages.Image{
		Name:      refString,
		Target:    manifestDesc,
		CreatedAt: createdAt,
	}
	if img.Name == "" {
		img.Name = danglingImageName(manifestDesc.Digest)
	}

	if err = i.createOrReplaceImage(ctx, img); err != nil {
		logger.WithError(err).Debug("failed to save image")
		return "", err
	}

	err = i.unpackImage(ctx, i.StorageDriver(), img, manifestDesc)
	if err != nil {
		logger.WithError(err).Debug("failed to unpack image")
	} else {
		i.LogImageEvent(ctx, id.String(), id.String(), events.ActionImport)
	}

	return id, err
}

// saveArchive saves the archive from bufRd to the content store, compressing it if necessary.
// Returns compressed blob digest, digest of the uncompressed data and media type of the stored blob.
func saveArchive(ctx context.Context, cs content.Store, layerReader io.Reader) (digest.Digest, digest.Digest, string, error) {
	// Wrap the reader in buffered reader to allow peeks.
	p := pools.BufioReader32KPool
	bufRd := p.Get(layerReader)
	defer p.Put(bufRd)

	comp, err := detectCompression(bufRd)
	if err != nil {
		return "", "", "", err
	}

	var uncompressedReader io.Reader = bufRd
	switch comp {
	case compression.Gzip, compression.Zstd:
		// If the input is already a compressed layer, just save it as is.
		mediaType := ocispec.MediaTypeImageLayerGzip
		if comp == compression.Zstd {
			mediaType = ocispec.MediaTypeImageLayerZstd
		}

		compressedDigest, uncompressedDigest, err := writeCompressedBlob(ctx, cs, mediaType, bufRd)
		if err != nil {
			return "", "", "", err
		}

		return compressedDigest, uncompressedDigest, mediaType, nil
	case compression.Bzip2, compression.Xz:
		r, err := compression.DecompressStream(bufRd)
		if err != nil {
			return "", "", "", errdefs.InvalidParameter(err)
		}
		defer r.Close()
		uncompressedReader = r
		fallthrough
	case compression.None:
		mediaType := ocispec.MediaTypeImageLayerGzip
		compressedDigest, uncompressedDigest, err := compressAndWriteBlob(ctx, cs, compression.Gzip, mediaType, uncompressedReader)
		if err != nil {
			return "", "", "", err
		}

		return compressedDigest, uncompressedDigest, mediaType, nil
	}

	return "", "", "", errdefs.InvalidParameter(errors.New("unsupported archive compression"))
}

// writeCompressedBlob writes the blob and simultaneously computes the digest of the uncompressed data.
func writeCompressedBlob(ctx context.Context, cs content.Store, mediaType string, bufRd *bufio.Reader) (digest.Digest, digest.Digest, error) {
	pr, pw := io.Pipe()
	defer pw.Close()
	defer pr.Close()

	c := make(chan digest.Digest)
	// Start copying the blob to the content store from the pipe and tee it to the pipe.
	go func() {
		compressedDigest, err := writeBlobAndReturnDigest(ctx, cs, mediaType, io.TeeReader(bufRd, pw))
		pw.CloseWithError(err)
		c <- compressedDigest
	}()

	digester := digest.Canonical.Digester()

	// Decompress the piped blob.
	decompressedStream, err := compression.DecompressStream(pr)
	if err == nil {
		// Feed the digester with decompressed data.
		_, err = io.Copy(digester.Hash(), decompressedStream)
		decompressedStream.Close()
	}
	pr.CloseWithError(err)

	compressedDigest := <-c
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return "", "", errdefs.Cancelled(err)
		}
		return "", "", errdefs.System(err)
	}

	uncompressedDigest := digester.Digest()
	return compressedDigest, uncompressedDigest, nil
}

// compressAndWriteBlob compresses the uncompressedReader and stores it in the content store.
func compressAndWriteBlob(ctx context.Context, cs content.Store, comp compression.Compression, mediaType string, uncompressedLayerReader io.Reader) (digest.Digest, digest.Digest, error) {
	pr, pw := io.Pipe()
	defer pr.Close()
	defer pw.Close()

	compressor, err := compression.CompressStream(pw, comp)
	if err != nil {
		return "", "", errdefs.InvalidParameter(err)
	}

	writeChan := make(chan digest.Digest)
	// Start copying the blob to the content store from the pipe.
	go func() {
		dgst, err := writeBlobAndReturnDigest(ctx, cs, mediaType, pr)
		pr.CloseWithError(err)
		writeChan <- dgst
	}()

	// Copy archive to the pipe and tee it to a digester.
	// This will feed the pipe the above goroutine is reading from.
	uncompressedDigester := digest.Canonical.Digester()
	readFromInputAndDigest := io.TeeReader(uncompressedLayerReader, uncompressedDigester.Hash())
	_, err = io.Copy(compressor, readFromInputAndDigest)
	compressor.Close()
	pw.CloseWithError(err)

	compressedDigest := <-writeChan
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return "", "", errdefs.Cancelled(err)
		}
		return "", "", errdefs.System(err)
	}

	return compressedDigest, uncompressedDigester.Digest(), err
}

// writeBlobAndReturnDigest writes a blob to the content store and returns the digest.
func writeBlobAndReturnDigest(ctx context.Context, cs content.Store, mt string, reader io.Reader) (digest.Digest, error) {
	digester := digest.Canonical.Digester()
	if err := content.WriteBlob(ctx, cs, uuid.New().String(), io.TeeReader(reader, digester.Hash()), ocispec.Descriptor{MediaType: mt}); err != nil {
		return "", errdefs.System(err)
	}
	return digester.Digest(), nil
}

// unpackImage unpacks the platform-specific manifest of a image into the snapshotter.
func (i *ImageService) unpackImage(ctx context.Context, snapshotter string, img c8dimages.Image, manifestDesc ocispec.Descriptor) error {
	c8dImg, err := i.NewImageManifest(ctx, img, manifestDesc)
	if err != nil {
		return err
	}

	if err := c8dImg.Unpack(ctx, snapshotter); err != nil {
		if !cerrdefs.IsAlreadyExists(err) {
			return errdefs.System(fmt.Errorf("failed to unpack image: %w", err))
		}
	}

	return nil
}

// detectCompression detects the reader compression type.
func detectCompression(bufRd *bufio.Reader) (compression.Compression, error) {
	bs, err := bufRd.Peek(10)
	if err != nil && err != io.EOF {
		// Note: we'll ignore any io.EOF error because there are some odd
		// cases where the layer.tar file will be empty (zero bytes) and
		// that results in an io.EOF from the Peek() call. So, in those
		// cases we'll just treat it as a non-compressed stream and
		// that means just create an empty layer.
		// See Issue 18170
		return compression.None, errdefs.Unknown(err)
	}

	return compression.Detect(bs), nil
}

// fillUncompressedLabel sets the uncompressed digest label on the compressed blob metadata
// and returns the compressed blob size.
func fillUncompressedLabel(ctx context.Context, cs content.Store, compressedDigest digest.Digest, uncompressedDigest digest.Digest) (int64, error) {
	info, err := cs.Info(ctx, compressedDigest)
	if err != nil {
		return 0, errdefs.Unknown(errors.Wrapf(err, "couldn't open previously written blob"))
	}
	size := info.Size
	info.Labels = map[string]string{"containerd.io/uncompressed": uncompressedDigest.String()}

	_, err = cs.Update(ctx, info, "labels.*")
	if err != nil {
		return 0, errdefs.System(errors.Wrapf(err, "couldn't set uncompressed label"))
	}
	return size, nil
}

// storeJson marshals the provided object as json and stores it.
func storeJson(ctx context.Context, cs content.Ingester, mt string, obj interface{}, labels map[string]string) (ocispec.Descriptor, error) {
	configData, err := json.Marshal(obj)
	if err != nil {
		return ocispec.Descriptor{}, errdefs.InvalidParameter(err)
	}
	configDigest := digest.FromBytes(configData)
	desc := ocispec.Descriptor{
		MediaType: mt,
		Digest:    configDigest,
		Size:      int64(len(configData)),
	}

	var opts []content.Opt
	if labels != nil {
		opts = append(opts, content.WithLabels(labels))
	}

	err = content.WriteBlob(ctx, cs, configDigest.String(), bytes.NewReader(configData), desc, opts...)
	if err != nil {
		return ocispec.Descriptor{}, errdefs.System(err)
	}
	return desc, nil
}
