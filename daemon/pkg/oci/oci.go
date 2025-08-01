package oci

import (
	"fmt"
	"strconv"

	"github.com/moby/moby/v2/daemon/internal/lazyregexp"
	"github.com/opencontainers/runtime-spec/specs-go"
)

// TODO verify if this regex is correct for "a" (all);
//
// The docs (https://github.com/torvalds/linux/blob/v5.10/Documentation/admin-guide/cgroup-v1/devices.rst) describe:
// "'all' means it applies to all types and all major and minor numbers", and shows an example
// that *only* passes `a` as value: `echo a > /sys/fs/cgroup/1/devices.allow, which would be
// the "implicit" equivalent of "a *:* rwm". Source-code also looks to confirm this, and returns
// early for "a" (all); https://github.com/torvalds/linux/blob/v5.10/security/device_cgroup.c#L614-L642
var deviceCgroupRuleRegex = lazyregexp.New("^([acb]) ([0-9]+|\\*):([0-9]+|\\*) ([rwm]{1,3})$")

// SetCapabilities sets the provided capabilities on the spec.
//
// Deprecated: this function is no longer used and will be removed in the next release.
func SetCapabilities(s *specs.Spec, caplist []string) error {
	if s.Process == nil {
		s.Process = &specs.Process{}
	}
	s.Process.Capabilities = &specs.LinuxCapabilities{
		Effective: caplist,
		Bounding:  caplist,
		Permitted: caplist,
	}
	return nil
}

// AppendDevicePermissionsFromCgroupRules takes rules for the devices cgroup to append to the default set
func AppendDevicePermissionsFromCgroupRules(devPermissions []specs.LinuxDeviceCgroup, rules []string) ([]specs.LinuxDeviceCgroup, error) {
	for _, deviceCgroupRule := range rules {
		ss := deviceCgroupRuleRegex.FindAllStringSubmatch(deviceCgroupRule, -1)
		if len(ss) == 0 || len(ss[0]) != 5 {
			return nil, fmt.Errorf("invalid device cgroup rule format: '%s'", deviceCgroupRule)
		}
		matches := ss[0]

		dPermissions := specs.LinuxDeviceCgroup{
			Allow:  true,
			Type:   matches[1],
			Access: matches[4],
		}
		if matches[2] == "*" {
			major := int64(-1)
			dPermissions.Major = &major
		} else {
			major, err := strconv.ParseInt(matches[2], 10, 64)
			if err != nil {
				return nil, fmt.Errorf("invalid major value in device cgroup rule format: '%s'", deviceCgroupRule)
			}
			dPermissions.Major = &major
		}
		if matches[3] == "*" {
			minor := int64(-1)
			dPermissions.Minor = &minor
		} else {
			minor, err := strconv.ParseInt(matches[3], 10, 64)
			if err != nil {
				return nil, fmt.Errorf("invalid minor value in device cgroup rule format: '%s'", deviceCgroupRule)
			}
			dPermissions.Minor = &minor
		}
		devPermissions = append(devPermissions, dPermissions)
	}
	return devPermissions, nil
}
