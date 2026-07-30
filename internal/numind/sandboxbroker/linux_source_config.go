package sandboxbroker

// LinuxHostSourceConfig contains only host paths and identities owned by the
// sandboxd deployment. It deliberately has no product DB/COS/LLM/Feishu fields.
type LinuxHostSourceConfig struct {
	ParentCgroupPath   string
	WorkloadCgroupPath string
	DataRootPath       string
	DataRootUUID       string
	ImageDigest        string

	ProcMeminfoPath       string
	CgroupControllersPath string
	DockerBinary          string
	FindmntBinary         string
}

func (c LinuxHostSourceConfig) withDefaults() LinuxHostSourceConfig {
	if c.ProcMeminfoPath == "" {
		c.ProcMeminfoPath = "/proc/meminfo"
	}
	if c.CgroupControllersPath == "" {
		c.CgroupControllersPath = "/sys/fs/cgroup/cgroup.controllers"
	}
	if c.DockerBinary == "" {
		c.DockerBinary = SandboxDockerBinary
	}
	if c.FindmntBinary == "" {
		c.FindmntBinary = "/usr/bin/findmnt"
	}
	return c
}
