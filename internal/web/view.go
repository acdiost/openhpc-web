package web

import (
	"strconv"
	"time"

	"github.com/openhpc-web/openhpc-web/internal/cluster"
	"github.com/openhpc-web/openhpc-web/internal/platform"
)

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
	EmptyPartitions, PartitionStatus, NodeStatus             string
	Node, Partition, State, CPUs, Memory, GRES, Availability string
	OnlineNodes, CPUUtilization                              string
	JobID, JobName, User, Account, Elapsed, TimeLimit        string
	NodesOrReason, Online, Offline, NodeCount                string
	JobDetails, BackToJobs, JobNotFound, Close               string
	CPUCount, SubmitTime, StartTime, WorkDir                 string
	EligibleTime, EndTime, Unknown                           string
	StdOut, StdErr, Command, ViewContent                     string
	OutputPreviewUnavailable, OutputLoading, OutputError     string
	OutputPreview, OutputTruncated, Details, Actions         string
	Resources, ResourceUsage, ResourceLoading, ResourceError string
	ResourceEmpty, SampledAt, CPUTime, MaxRSS, ResourceTrend string
	Step, AveCPU, TotalCPU, AveRSS, MaxVMSize                string
	Accounts, Users, Description, Organization               string
	Coordinators, Associations, AdminLevel                   string
	AssociationDetails, AssociationID, Cluster               string
	AccountLevel, AllPartitions, EmptyAssociations           string
	AssociationUnavailable                                   string
	PreviousPage, NextPage                                   string
	DefaultAccount, DefaultWCKey, Priority, UsageFactor      string
	MaxJobs, Unlimited                                       string
}

type accountsView struct {
	appChrome
	Module                  module
	Labels                  detailCopy
	Directory               cluster.AccountDirectory
	Associations            []cluster.Association
	AssociationsAvailable   bool
	AssociationPreviousPage int
	AssociationNextPage     int
}
type qosView struct {
	appChrome
	Module module
	Labels detailCopy
	QoS    []cluster.QoS
}

type nodesView struct {
	appChrome
	Module              module
	Labels              detailCopy
	Nodes               []cluster.Node
	Partitions          []cluster.Partition
	NodesAvailable      bool
	PartitionsAvailable bool
}

type jobsView struct {
	appChrome
	Module     module
	Labels     detailCopy
	Jobs       []cluster.Job
	JobDetails []jobModalView
}

type jobModalView struct {
	Labels        detailCopy
	Job           cluster.Job
	EndTime       string
	CanViewStdOut bool
	CanViewStdErr bool
}

type auditCopy struct {
	Description, Refresh, Actor, Action, Outcome, Time string
	Empty, Unavailable, Older                          string
}

type auditView struct {
	appChrome
	Labels         auditCopy
	Events         []auditEventView
	AuditAvailable bool
	HasMore        bool
	NextBeforeID   int64
}

type auditEventView struct {
	ID, Actor, Action, Outcome, CreatedAt, OutcomeClass string
}

func newAuditEventView(event platform.AuditEvent) auditEventView {
	return auditEventView{
		ID: strconv.FormatInt(event.ID, 10), Actor: event.Actor, Action: event.Action,
		Outcome: event.Outcome, CreatedAt: event.CreatedAt.UTC().Format(time.RFC3339),
		OutcomeClass: auditOutcomeClass(event.Outcome),
	}
}

func auditOutcomeClass(outcome string) string {
	switch outcome {
	case "success":
		return "success"
	case "denied", "rate_limited", "unavailable":
		return "denied"
	case "cancelled", "timeout":
		return "warning"
	default:
		return "neutral"
	}
}

func auditCopyFor(language string) auditCopy {
	if language == "en" {
		return auditCopy{
			Description: "Recent security and operations events", Refresh: "Refresh",
			Actor: "Actor", Action: "Action", Outcome: "Outcome", Time: "Time (UTC)",
			Empty: "No audit events", Unavailable: "Audit log is temporarily unavailable", Older: "Older events",
		}
	}
	return auditCopy{
		Description: "最近的安全与运维事件", Refresh: "刷新",
		Actor: "操作者", Action: "动作", Outcome: "结果", Time: "UTC 时间",
		Empty: "暂无审计记录", Unavailable: "审计日志暂不可用", Older: "更早记录",
	}
}

func detailCopyFor(language string) detailCopy {
	if language == "en" {
		return detailCopy{
			Refresh: "Refresh", LiveData: "Live Slurm data", EmptyNodes: "No nodes reported", EmptyJobs: "No jobs in the queue",
			EmptyPartitions: "No partitions reported", PartitionStatus: "Partition status", NodeStatus: "Node status",
			Node: "Node", Partition: "Partition", State: "State", CPUs: "CPUs", Memory: "Memory", GRES: "GRES", Availability: "Availability",
			OnlineNodes: "Online nodes", CPUUtilization: "CPU utilization",
			JobID: "Job ID", JobName: "Name", User: "User", Account: "Account", Elapsed: "Elapsed", TimeLimit: "Time limit",
			NodesOrReason: "Nodes / reason", Online: "Online", Offline: "Unavailable", NodeCount: "Node count",
			JobDetails: "Job details", BackToJobs: "Back to jobs", JobNotFound: "Job not found in the current queue", Close: "Close",
			Resources: "Resources", ResourceUsage: "Live resource usage", ResourceLoading: "Loading sstat data...", ResourceError: "Resource data is temporarily unavailable",
			ResourceEmpty: "No active sstat steps reported", SampledAt: "Sampled at", CPUTime: "CPU time", MaxRSS: "Maximum RSS", ResourceTrend: "Recent resource trend",
			Step: "Step", AveCPU: "Average CPU time", TotalCPU: "Total CPU time", AveRSS: "Average RSS", MaxVMSize: "Maximum virtual memory",
			CPUCount: "CPU count", SubmitTime: "Submit time", StartTime: "Start time", WorkDir: "Working directory",
			EligibleTime: "EligibleTime", EndTime: "EndTime", Unknown: "Unknown",
			StdOut: "Standard output", StdErr: "Standard error", Command: "Submit command", ViewContent: "View content",
			OutputPreviewUnavailable: "Output preview is not enabled", OutputLoading: "Loading output...", OutputError: "Output could not be loaded",
			OutputPreview: "Output preview", OutputTruncated: "latest 256 KiB only", Details: "Details", Actions: "Actions",
			Accounts: "Accounts", Users: "Users", Description: "Description", Organization: "Organization", Coordinators: "Coordinators", Associations: "Associations", AdminLevel: "Admin level",
			AssociationDetails: "Association details", AssociationID: "ID", Cluster: "Cluster", AccountLevel: "Account level", AllPartitions: "All partitions",
			EmptyAssociations: "No associations reported", AssociationUnavailable: "Association data is temporarily unavailable",
			PreviousPage: "Previous page", NextPage: "Next page",
			DefaultAccount: "Default account", DefaultWCKey: "Default WCKey", Priority: "Priority", UsageFactor: "Usage factor", MaxJobs: "Max jobs", Unlimited: "Unlimited",
		}
	}
	return detailCopy{
		Refresh: "刷新", LiveData: "Slurm 实时数据", EmptyNodes: "Slurm 未报告节点", EmptyJobs: "当前队列中没有作业",
		EmptyPartitions: "Slurm 未报告分区", PartitionStatus: "分区状态", NodeStatus: "节点状态",
		Node: "节点", Partition: "分区", State: "状态", CPUs: "CPU", Memory: "内存", GRES: "GRES", Availability: "可用性",
		OnlineNodes: "在线节点", CPUUtilization: "CPU 利用率",
		JobID: "作业 ID", JobName: "名称", User: "用户", Account: "账户", Elapsed: "已运行", TimeLimit: "时间限制",
		NodesOrReason: "节点 / 原因", Online: "在线", Offline: "不可用", NodeCount: "节点数",
		JobDetails: "作业详细信息", BackToJobs: "返回作业列表", JobNotFound: "当前队列中未找到该作业", Close: "关闭",
		Resources: "资源", ResourceUsage: "实时资源消耗", ResourceLoading: "正在加载 sstat 数据...", ResourceError: "资源数据暂不可用",
		ResourceEmpty: "sstat 暂无活动步骤数据", SampledAt: "采样时间", CPUTime: "CPU 时间", MaxRSS: "最大常驻内存", ResourceTrend: "近期资源趋势",
		Step: "步骤", AveCPU: "平均 CPU 时间", TotalCPU: "总 CPU 时间", AveRSS: "平均常驻内存", MaxVMSize: "最大虚拟内存",
		CPUCount: "CPU数", SubmitTime: "提交时间", StartTime: "开始时间", WorkDir: "工作目录",
		EligibleTime: "可调度时间", EndTime: "结束时间", Unknown: "未知",
		StdOut: "标准输出", StdErr: "标准错误", Command: "提交命令", ViewContent: "查看内容",
		OutputPreviewUnavailable: "未启用输出内容预览", OutputLoading: "正在加载输出...", OutputError: "无法加载输出内容",
		OutputPreview: "输出内容", OutputTruncated: "仅显示末尾 256 KiB", Details: "详情", Actions: "操作",
		Accounts: "账户", Users: "用户", Description: "描述", Organization: "组织", Coordinators: "协调员", Associations: "关联数", AdminLevel: "管理员级别",
		AssociationDetails: "关联明细", AssociationID: "ID", Cluster: "集群", AccountLevel: "账户级", AllPartitions: "全部分区",
		EmptyAssociations: "暂无关联记录", AssociationUnavailable: "关联数据暂不可用",
		PreviousPage: "上一页", NextPage: "下一页",
		DefaultAccount: "默认账户", DefaultWCKey: "默认 WCKey", Priority: "优先级", UsageFactor: "使用因子", MaxJobs: "最大作业数", Unlimited: "无限制",
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
