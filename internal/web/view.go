package web

import "github.com/openhpc-web/openhpc-web/internal/cluster"

type loginView struct {
	Language  string
	Theme     string
	PageTitle string
	Next      string
	Error     string
}

type copySet struct {
	Overview, OnlineNodes, RunningJobs, QueuedJobs, CPUUsage string
	Search, Notifications, Language, Theme, SignOut          string
	SystemHealthy, UpdatedNow, ClusterStatus, QuickAccess    string
	ComingSoon, ComingSoonDetail                             string
	Utilization, Partitions, ReportedBySlurm                 string
	AcrossPartitions, CurrentAverage, MedianWait             string
	PlatformAdmin, Operations, Navigation, Menu, ChartLabel  string
	DashboardLabel, NowLabel, SlurmController                string
	SlurmUnavailable                                         string
	CurrentSnapshot                                          string
}

type module struct {
	Path, Label, Group, Icon string
}

type pageHeading struct {
	Eyebrow         string
	Title           string
	Description     string
	RefreshPath     string
	RefreshLabel    string
	Status          string
	StatusAvailable bool
}

type appChrome struct {
	Language   string
	Theme      string
	Username   string
	CSRFToken  string
	PageTitle  string
	ActivePath string
	Available  bool
	Copy       copySet
	Modules    []module
	Heading    pageHeading
}

type dashboardView struct {
	appChrome
	Metrics          DashboardMetrics
	MetricsAvailable bool
}

type moduleView struct {
	appChrome
	Module module
}

type detailCopy struct {
	Refresh, LiveData, EmptyNodes, EmptyJobs                 string
	Node, Partition, State, CPUs, Memory, GRES, Availability string
	JobID, JobName, User, Account, Elapsed, TimeLimit        string
	NodesOrReason, Online, Offline                           string
}

type nodesView struct {
	appChrome
	Module module
	Labels detailCopy
	Nodes  []cluster.Node
}

type jobsView struct {
	appChrome
	Module module
	Labels detailCopy
	Jobs   []cluster.Job
}

func detailCopyFor(language string) detailCopy {
	if language == "en" {
		return detailCopy{
			Refresh: "Refresh", LiveData: "Live Slurm data", EmptyNodes: "No nodes reported", EmptyJobs: "No jobs in the queue",
			Node: "Node", Partition: "Partition", State: "State", CPUs: "CPUs", Memory: "Memory", GRES: "GRES", Availability: "Availability",
			JobID: "Job ID", JobName: "Name", User: "User", Account: "Account", Elapsed: "Elapsed", TimeLimit: "Time limit",
			NodesOrReason: "Nodes / reason", Online: "Online", Offline: "Unavailable",
		}
	}
	return detailCopy{
		Refresh: "刷新", LiveData: "Slurm 实时数据", EmptyNodes: "Slurm 未报告节点", EmptyJobs: "当前队列中没有作业",
		Node: "节点", Partition: "分区", State: "状态", CPUs: "CPU", Memory: "内存", GRES: "GRES", Availability: "可用性",
		JobID: "作业 ID", JobName: "名称", User: "用户", Account: "账户", Elapsed: "已运行", TimeLimit: "时间限制",
		NodesOrReason: "节点 / 原因", Online: "在线", Offline: "不可用",
	}
}

func copyFor(language string) copySet {
	if language == "en" {
		return copySet{
			Overview: "Cluster Overview", OnlineNodes: "Online Nodes", RunningJobs: "Running Jobs", QueuedJobs: "Queued Jobs", CPUUsage: "CPU Utilization",
			Search: "Search modules", Notifications: "Notifications", Language: "Language", Theme: "Theme", SignOut: "Sign out",
			SystemHealthy: "All systems operational", UpdatedNow: "Updated just now", ClusterStatus: "Cluster status", QuickAccess: "Quick access",
			ComingSoon: "Integration pending", ComingSoonDetail: "This module boundary is ready for its Slurm or LDAP adapter.",
			Utilization: "Compute utilization", Partitions: "Partitions", ReportedBySlurm: "Reported by Slurm",
			AcrossPartitions: "across 6 partitions", CurrentAverage: "current cluster average", MedianWait: "median wait 4m 12s",
			PlatformAdmin: "Platform admin", Operations: "Operations", Navigation: "Primary navigation", Menu: "Menu", ChartLabel: "CPU and GPU utilization chart",
			DashboardLabel: "DASHBOARD", NowLabel: "NOW", SlurmController: "Slurm controller",
			SlurmUnavailable: "Slurm data is temporarily unavailable",
			CurrentSnapshot:  "Current scheduler snapshot",
		}
	}
	return copySet{
		Overview: "集群概览", OnlineNodes: "在线节点", RunningJobs: "运行作业", QueuedJobs: "排队作业", CPUUsage: "CPU 利用率",
		Search: "搜索模块", Notifications: "通知", Language: "语言", Theme: "主题", SignOut: "退出登录",
		SystemHealthy: "所有系统运行正常", UpdatedNow: "刚刚更新", ClusterStatus: "集群状态", QuickAccess: "快捷入口",
		ComingSoon: "等待系统集成", ComingSoonDetail: "模块边界已经就绪，可在下一阶段接入 Slurm 或 LDAP 适配器。",
		Utilization: "计算资源利用率", Partitions: "分区状态", ReportedBySlurm: "由 Slurm 实时报告",
		AcrossPartitions: "覆盖 6 个分区", CurrentAverage: "当前集群平均值", MedianWait: "等待中位数 4 分 12 秒",
		PlatformAdmin: "平台管理员", Operations: "运维操作", Navigation: "主导航", Menu: "菜单", ChartLabel: "CPU 和 GPU 利用率图表",
		DashboardLabel: "总览", NowLabel: "当前", SlurmController: "Slurm 控制器",
		SlurmUnavailable: "Slurm 数据暂不可用",
		CurrentSnapshot:  "当前调度快照",
	}
}

func modulesFor(language string) []module {
	zh := []module{
		{Path: "/dashboard", Label: "总览", Group: "工作台", Icon: "grid"},
		{Path: "/slurm/nodes", Label: "节点与分区", Group: "Slurm", Icon: "server"},
		{Path: "/slurm/jobs", Label: "作业管理", Group: "Slurm", Icon: "jobs"},
		{Path: "/slurm/accounts", Label: "账户与用户", Group: "Slurm", Icon: "users"},
		{Path: "/slurm/qos", Label: "QoS 与核时", Group: "Slurm", Icon: "gauge"},
		{Path: "/ldap", Label: "LDAP 目录", Group: "身份", Icon: "directory"},
		{Path: "/platform/users", Label: "平台用户", Group: "身份", Icon: "shield"},
		{Path: "/slurm/config", Label: "Slurm 配置", Group: "系统", Icon: "settings"},
		{Path: "/system/files", Label: "文件管理", Group: "系统", Icon: "folder"},
		{Path: "/terminal", Label: "终端", Group: "系统", Icon: "terminal"},
		{Path: "/audit", Label: "审计日志", Group: "系统", Icon: "audit"},
	}
	if language != "en" {
		return zh
	}
	labels := []string{"Overview", "Nodes & partitions", "Jobs", "Accounts & users", "QoS & core hours", "LDAP directory", "Platform users", "Slurm configuration", "Files", "Terminal", "Audit log"}
	groups := []string{"Workspace", "Slurm", "Slurm", "Slurm", "Slurm", "Identity", "Identity", "System", "System", "System", "System"}
	result := make([]module, len(zh))
	for index, item := range zh {
		result[index] = module{Path: item.Path, Label: labels[index], Group: groups[index], Icon: item.Icon}
	}
	return result
}

func moduleByPath(path, language string) module {
	for _, item := range modulesFor(language) {
		if item.Path == path {
			return item
		}
	}
	labels := map[string][2]string{
		"/slurm/partitions":   {"分区管理", "Partitions"},
		"/slurm/users":        {"Slurm 用户", "Slurm users"},
		"/slurm/associations": {"关联管理", "Associations"},
		"/slurm/core-hours":   {"核时管理", "Core hours"},
	}
	if label, exists := labels[path]; exists {
		index := 0
		group := "Slurm"
		if language == "en" {
			index = 1
		}
		return module{Path: path, Label: label[index], Group: group, Icon: "settings"}
	}
	return module{Path: path, Label: path, Group: "System", Icon: "settings"}
}
