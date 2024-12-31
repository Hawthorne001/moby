//go:build !windows

package runconfig // import "github.com/docker/docker/runconfig"

import (
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/sysinfo"
	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"
)

func TestValidateResources(t *testing.T) {
	type resourceTest struct {
		doc                string
		resources          container.Resources
		sysInfoCPURealtime bool
		expectedError      string
	}

	tests := []resourceTest{
		{
			doc: "empty configuration",
		},
		{
			doc: "valid configuration",
			resources: container.Resources{
				CPURealtimePeriod:  1000,
				CPURealtimeRuntime: 1000,
			},
			sysInfoCPURealtime: true,
		},
		{
			doc: "cpu-rt-period not supported",
			resources: container.Resources{
				CPURealtimePeriod: 5000,
			},
			expectedError: "kernel does not support CPU real-time scheduler",
		},
		{
			doc: "cpu-rt-runtime not supported",
			resources: container.Resources{
				CPURealtimeRuntime: 5000,
			},
			expectedError: "kernel does not support CPU real-time scheduler",
		},
		{
			doc: "cpu-rt-runtime greater than cpu-rt-period",
			resources: container.Resources{
				CPURealtimePeriod:  5000,
				CPURealtimeRuntime: 10000,
			},
			sysInfoCPURealtime: true,
			expectedError:      "cpu real-time runtime cannot be higher than cpu real-time period",
		},
	}

	for _, tc := range tests {
		t.Run(tc.doc, func(t *testing.T) {
			var hc container.HostConfig
			hc.Resources = tc.resources

			var si sysinfo.SysInfo
			si.CPURealtime = tc.sysInfoCPURealtime

			err := validateResources(&hc, &si)
			if tc.expectedError != "" {
				assert.Check(t, is.Error(err, tc.expectedError))
			} else {
				assert.NilError(t, err)
			}
		})
	}
}
