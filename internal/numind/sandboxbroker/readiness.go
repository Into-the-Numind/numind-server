package sandboxbroker

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
)

const (
	// ReadinessDiskWarningPercent emits an alert without stopping admission.
	ReadinessDiskWarningPercent int64 = 70
	// ReadinessDiskBlockPercent stops admission and image pulls.
	ReadinessDiskBlockPercent int64 = 85
)

var (
	// ErrInvalidReadinessConfig means a root-owned expected value is unsafe.
	ErrInvalidReadinessConfig = errors.New("invalid sandbox readiness config")
	// ErrReadinessUnavailable means infrastructure cannot safely accept work.
	ErrReadinessUnavailable = errors.New("sandbox readiness unavailable")
)

// ReadinessCode is a bounded health reason without paths or host details.
type ReadinessCode string

const (
	ReadinessProbeUnavailable        ReadinessCode = "probe_unavailable"
	ReadinessRuntimeUnavailable      ReadinessCode = "runtime_unavailable"
	ReadinessCgroupUnavailable       ReadinessCode = "cgroup_v2_unavailable"
	ReadinessCgroupControllerMissing ReadinessCode = "cgroup_controller_missing"
	ReadinessCgroupParentMismatch    ReadinessCode = "cgroup_parent_mismatch"
	ReadinessCgroupLimitMismatch     ReadinessCode = "cgroup_limit_mismatch"
	ReadinessDataRootUnmounted       ReadinessCode = "data_root_unmounted"
	ReadinessDataRootMismatch        ReadinessCode = "data_root_mismatch"
	ReadinessDiskProbeInvalid        ReadinessCode = "disk_probe_invalid"
	ReadinessDiskBytesWarning        ReadinessCode = "disk_bytes_warning"
	ReadinessDiskInodesWarning       ReadinessCode = "disk_inodes_warning"
	ReadinessDiskBytesBlocked        ReadinessCode = "disk_bytes_blocked"
	ReadinessDiskInodesBlocked       ReadinessCode = "disk_inodes_blocked"
	ReadinessImageMismatch           ReadinessCode = "image_mismatch"
	ReadinessPressureBlocked         ReadinessCode = "pressure_blocked"
)

// ReadinessConfig contains only root-owned expected identities.
type ReadinessConfig struct {
	ParentCgroupPath   string
	WorkloadCgroupPath string
	DataRootPath       string
	DataRootUUID       string
	ImageDigest        string
}

// ReadinessSnapshot is one trusted host probe result.
type ReadinessSnapshot struct {
	RuntimeReady           bool
	CgroupV2               bool
	Controllers            map[string]bool
	ParentCgroupPath       string
	WorkloadCgroupPath     string
	ParentMemoryMaxBytes   int64
	WorkloadMemoryMaxBytes int64
	DataRootMounted        bool
	DataRootPath           string
	DataRootUUID           string
	DataRootBytesTotal     int64
	DataRootBytesUsed      int64
	DataRootInodesTotal    int64
	DataRootInodesUsed     int64
	ImageDigest            string
}

// ReadinessSource performs Linux-specific cgroup, mount, disk, and image probes.
type ReadinessSource interface {
	Snapshot(context.Context) (ReadinessSnapshot, error)
}

type readinessAdmissionGate interface {
	AdmissionStatus() (bool, PressureReason, uint64)
	PublishAdmissionIfCurrent(
		uint64,
		func(bool, PressureReason),
	) bool
}

// ReadinessResult contains only bounded reason codes.
type ReadinessResult struct {
	Ready              bool
	PressureReason     PressureReason
	PressureGeneration uint64
	Alerts             []ReadinessCode
	Failures           []ReadinessCode
}

// ReadinessChecker compares actual infrastructure with sealed expected values.
type ReadinessChecker struct {
	syncMu sync.Mutex

	config            ReadinessConfig
	source            ReadinessSource
	pressure          readinessAdmissionGate
	parentMemoryMax   int64
	workloadMemoryMax int64
}

// NewReadinessChecker rejects blocked capacity plans and unsafe identities.
func NewReadinessChecker(
	config ReadinessConfig,
	plan CapacityPlan,
	source ReadinessSource,
	pressure readinessAdmissionGate,
) (*ReadinessChecker, error) {
	if source == nil || pressure == nil || !validReadinessConfig(config) {
		return nil, ErrInvalidReadinessConfig
	}
	values, err := plan.SystemdValues()
	if err != nil {
		return nil, ErrInvalidReadinessConfig
	}
	parent := values["NUMIND_SANDBOX_PARENT_MEMORY_MAX_BYTES"]
	workload := values["NUMIND_SANDBOX_WORKLOAD_MEMORY_MAX_BYTES"]
	if parent <= 0 || workload <= 0 {
		return nil, ErrInvalidReadinessConfig
	}
	return &ReadinessChecker{
		config:            config,
		source:            source,
		pressure:          pressure,
		parentMemoryMax:   parent,
		workloadMemoryMax: workload,
	}, nil
}

// Check fails closed on any source error or identity mismatch.
func (c *ReadinessChecker) Check(
	ctx context.Context,
) (ReadinessResult, error) {
	if c == nil {
		return ReadinessResult{
			Failures: []ReadinessCode{ReadinessProbeUnavailable},
		}, ErrInvalidReadinessConfig
	}
	snapshot, err := c.source.Snapshot(ctx)
	if err != nil {
		return ReadinessResult{
			Failures: []ReadinessCode{ReadinessProbeUnavailable},
		}, err
	}
	result := ReadinessResult{}
	if !snapshot.RuntimeReady {
		addReadinessCode(&result.Failures, ReadinessRuntimeUnavailable)
	}
	if !snapshot.CgroupV2 {
		addReadinessCode(&result.Failures, ReadinessCgroupUnavailable)
	}
	for _, controller := range []string{"memory", "cpu", "pids", "io"} {
		if !snapshot.Controllers[controller] {
			addReadinessCode(
				&result.Failures,
				ReadinessCgroupControllerMissing,
			)
			break
		}
	}
	if snapshot.ParentCgroupPath != c.config.ParentCgroupPath ||
		snapshot.WorkloadCgroupPath != c.config.WorkloadCgroupPath {
		addReadinessCode(
			&result.Failures,
			ReadinessCgroupParentMismatch,
		)
	}
	if snapshot.ParentMemoryMaxBytes != c.parentMemoryMax ||
		snapshot.WorkloadMemoryMaxBytes != c.workloadMemoryMax {
		addReadinessCode(
			&result.Failures,
			ReadinessCgroupLimitMismatch,
		)
	}
	if !snapshot.DataRootMounted {
		addReadinessCode(&result.Failures, ReadinessDataRootUnmounted)
	}
	if snapshot.DataRootPath != c.config.DataRootPath ||
		snapshot.DataRootUUID != c.config.DataRootUUID {
		addReadinessCode(&result.Failures, ReadinessDataRootMismatch)
	}
	if !validDiskUsage(
		snapshot.DataRootBytesUsed,
		snapshot.DataRootBytesTotal,
	) || !validDiskUsage(
		snapshot.DataRootInodesUsed,
		snapshot.DataRootInodesTotal,
	) {
		addReadinessCode(&result.Failures, ReadinessDiskProbeInvalid)
	} else {
		if usageAtLeastPercent(
			snapshot.DataRootBytesUsed,
			snapshot.DataRootBytesTotal,
			ReadinessDiskWarningPercent,
		) {
			addReadinessCode(&result.Alerts, ReadinessDiskBytesWarning)
		}
		if usageAtLeastPercent(
			snapshot.DataRootInodesUsed,
			snapshot.DataRootInodesTotal,
			ReadinessDiskWarningPercent,
		) {
			addReadinessCode(&result.Alerts, ReadinessDiskInodesWarning)
		}
		if usageAtLeastPercent(
			snapshot.DataRootBytesUsed,
			snapshot.DataRootBytesTotal,
			ReadinessDiskBlockPercent,
		) {
			addReadinessCode(&result.Failures, ReadinessDiskBytesBlocked)
		}
		if usageAtLeastPercent(
			snapshot.DataRootInodesUsed,
			snapshot.DataRootInodesTotal,
			ReadinessDiskBlockPercent,
		) {
			addReadinessCode(&result.Failures, ReadinessDiskInodesBlocked)
		}
	}
	if snapshot.ImageDigest != c.config.ImageDigest {
		addReadinessCode(&result.Failures, ReadinessImageMismatch)
	}
	pressureAllowed, pressureReason, pressureGeneration :=
		c.pressure.AdmissionStatus()
	result.PressureReason = pressureReason
	result.PressureGeneration = pressureGeneration
	if !pressureAllowed {
		addReadinessCode(&result.Failures, ReadinessPressureBlocked)
	}
	result.Ready = len(result.Failures) == 0
	return result, nil
}

// RequireAdmission is the CreateLease gate used by the T10 composition root.
func (c *ReadinessChecker) RequireAdmission(ctx context.Context) error {
	result, err := c.Check(ctx)
	return readinessAdmissionError(result, err)
}

func readinessAdmissionError(
	result ReadinessResult,
	err error,
) error {
	if err != nil {
		return err
	}
	if !result.Ready {
		if len(result.Failures) == 1 &&
			result.Failures[0] == ReadinessPressureBlocked &&
			isCapacityPressureReason(result.PressureReason) {
			return ErrSchedulerActiveLimit
		}
		return ErrReadinessUnavailable
	}
	return nil
}

// SyncAdmission is the single bridge from probed readiness to the scheduler's
// atomically enforced FIFO gate. The T10 sampler/watchdog owns its cadence.
func (c *ReadinessChecker) SyncAdmission(
	ctx context.Context,
	scheduler *Scheduler,
) error {
	if c == nil || scheduler == nil {
		return ErrInvalidReadinessConfig
	}
	c.syncMu.Lock()
	defer c.syncMu.Unlock()
	result, checkErr := c.Check(ctx)
	err := readinessAdmissionError(result, checkErr)
	if err != nil {
		scheduler.SetAdmission(false, err)
		return err
	}
	if !c.pressure.PublishAdmissionIfCurrent(
		result.PressureGeneration,
		func(allowed bool, _ PressureReason) {
			if allowed {
				scheduler.SetAdmission(true, nil)
				return
			}
			scheduler.SetAdmission(false, ErrReadinessUnavailable)
		},
	) {
		scheduler.SetAdmission(false, ErrReadinessUnavailable)
		return ErrReadinessUnavailable
	}
	return nil
}

func isCapacityPressureReason(reason PressureReason) bool {
	switch reason {
	case PressureReasonWorkloadHigh,
		PressureReasonWorkloadShed,
		PressureReasonHostLow,
		PressureReasonHostEmergency:
		return true
	default:
		return false
	}
}

func validReadinessConfig(config ReadinessConfig) bool {
	return safeReadinessPath(config.ParentCgroupPath, "/sys/fs/cgroup/") &&
		safeReadinessPath(config.WorkloadCgroupPath, "/sys/fs/cgroup/") &&
		strictPathDescendant(
			config.ParentCgroupPath,
			config.WorkloadCgroupPath,
		) &&
		safeReadinessPath(config.DataRootPath, "/opt/numind-sandbox/") &&
		validFilesystemUUID(config.DataRootUUID) &&
		validPinnedImage(config.ImageDigest)
}

func strictPathDescendant(parent string, child string) bool {
	relative, err := filepath.Rel(parent, child)
	if err != nil || relative == "." || filepath.IsAbs(relative) {
		return false
	}
	return relative != ".." &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func safeReadinessPath(value string, prefix string) bool {
	return filepath.IsAbs(value) &&
		filepath.Clean(value) == value &&
		value != string(filepath.Separator) &&
		strings.HasPrefix(value, prefix) &&
		!strings.ContainsRune(value, 0)
}

func validFilesystemUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	nonzero := false
	for index, char := range value {
		switch index {
		case 8, 13, 18, 23:
			if char != '-' {
				return false
			}
		default:
			if (char < '0' || char > '9') &&
				(char < 'a' || char > 'f') {
				return false
			}
			if char != '0' {
				nonzero = true
			}
		}
	}
	return nonzero
}

func validDiskUsage(used int64, total int64) bool {
	return total > 0 && used >= 0 && used <= total
}

func usageAtLeastPercent(
	used int64,
	total int64,
	percent int64,
) bool {
	if !validDiskUsage(used, total) || percent <= 0 || percent > 100 {
		return false
	}
	threshold := total/100*percent +
		(total%100*percent+99)/100
	return used >= threshold
}

func addReadinessCode(target *[]ReadinessCode, code ReadinessCode) {
	for _, existing := range *target {
		if existing == code {
			return
		}
	}
	*target = append(*target, code)
}
