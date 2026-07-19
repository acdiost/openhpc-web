package web

import (
	"fmt"
	"strconv"
	"time"

	"github.com/acdiost/openhpc-web/internal/cluster"
	"github.com/acdiost/openhpc-web/internal/directory"
	"github.com/acdiost/openhpc-web/internal/platform"
	"github.com/acdiost/openhpc-web/internal/slurmconfig"
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
	RoleLabel  string
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

type slurmConfigCopy struct {
	Description, Refresh, Files, ReadOnly, SelectFile, Empty, Unavailable, Truncated string
}

type slurmConfigFileView struct {
	Name      string
	Size      int64
	Content   string
	Truncated bool
}

type slurmConfigView struct {
	appChrome
	Module    module
	Labels    slurmConfigCopy
	Entries   []slurmconfig.Entry
	Selected  *slurmConfigFileView
	Available bool
}

type platformUserRow struct {
	Username, Role, CreatedAt string
	Enabled                   bool
}
type platformUsersView struct {
	appChrome
	Users          []platformUserRow
	Error, Success string
}

func slurmConfigCopyFor(language string) slurmConfigCopy {
	if language == "en" {
		return slurmConfigCopy{Description: "Read-only Slurm configuration files", Refresh: "Refresh", Files: "Configuration files", ReadOnly: "Read-only", SelectFile: "Select a file to inspect", Empty: "No configuration files reported", Unavailable: "Slurm configuration is temporarily unavailable", Truncated: "File content was truncated to the configured limit"}
	}
	return slurmConfigCopy{Description: "只读 Slurm 配置文件", Refresh: "刷新", Files: "配置文件", ReadOnly: "只读", SelectFile: "选择文件查看内容", Empty: "没有可用的配置文件", Unavailable: "Slurm 配置暂不可用", Truncated: "文件内容已截断至配置的大小限制"}
}

type detailCopy struct {
	Refresh, LiveData, EmptyNodes, EmptyJobs                 string
	EmptyPartitions, PartitionStatus, NodeStatus             string
	Node, Partition, State, CPUs, Memory, GRES, Availability string
	OnlineNodes, TotalNodes, OfflineNodes, CPUUtilization    string
	JobID, JobName, User, Account, Elapsed, TimeLimit        string
	NodesOrReason, Online, Offline, NodeCount                string
	BringNodeOnline, TakeNodeOffline                         string
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

type partitionCopy struct {
	Description, Refresh, EmptyNodes, EmptyPartitions string
	Unavailable, PartitionName, SelectedNodes         string
	CreatePartition, UpdatePartition                  string
	LiveNodes, SystemPartitions, PlatformPartitions   string
	Status, ReadOnly, Source, ImportedAt              string
	EditPartition, DeletePartition, SavePartition     string
	Matched, Added, Removed                           string
	Created, Patched, Unchanged, Deleted              string
	Node, Partition, State, Action                    string
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
	Module         module
	Labels         detailCopy
	QoS            []cluster.QoS
	ShowCoreHours  bool
	CoreLabels     coreHourCopy
	CoreHours      coreHourSummaryView
	SelectedPeriod cluster.CoreHourPeriod
}

type coreHourCopy struct {
	QoSTab, CoreHoursTab, Period24Hours, Period7Days, Period30Days string
	AllocatedCoreHours, Allocations, ByAccount, ByUser, Name       string
	Definition, Empty                                              string
}

type coreHourGroupView struct {
	Name, CoreHours string
	AllocationCount int
}

type coreHourSummaryView struct {
	CoreHours       string
	AllocationCount int
	Accounts        []coreHourGroupView
	Users           []coreHourGroupView
}

func coreHourCopyFor(language string) coreHourCopy {
	if language == "en" {
		return coreHourCopy{
			QoSTab: "QoS", CoreHoursTab: "Core-hour statistics", Period24Hours: "Past 24 hours", Period7Days: "Past 7 days", Period30Days: "Past 30 days",
			AllocatedCoreHours: "Allocated CPU core-hours", Allocations: "Allocations", ByAccount: "By account", ByUser: "By user", Name: "Name",
			Definition: "Allocated CPUs multiplied by wall-clock time within the selected window. GPU/TRES billing and actual CPU utilization are excluded.", Empty: "No CPU allocations reported",
		}
	}
	return coreHourCopy{
		QoSTab: "QoS", CoreHoursTab: "核时统计", Period24Hours: "过去 24 小时", Period7Days: "过去 7 天", Period30Days: "过去 30 天",
		AllocatedCoreHours: "分配 CPU 核时", Allocations: "作业分配数", ByAccount: "按账户", ByUser: "按用户", Name: "名称",
		Definition: "分配 CPU 数乘以所选时间窗口内的墙钟占用时间；不包含 GPU/TRES 计费，也不代表 CPU 实际利用率。", Empty: "所选周期内没有 CPU allocation",
	}
}

func newCoreHourSummaryView(summary cluster.CoreHourSummary) coreHourSummaryView {
	return coreHourSummaryView{
		CoreHours: formatCoreHours(summary.CoreSeconds), AllocationCount: summary.AllocationCount,
		Accounts: newCoreHourGroupViews(summary.Accounts), Users: newCoreHourGroupViews(summary.Users),
	}
}

func newCoreHourGroupViews(values []cluster.CoreHourGroup) []coreHourGroupView {
	result := make([]coreHourGroupView, len(values))
	for index, value := range values {
		result[index] = coreHourGroupView{Name: value.Name, CoreHours: formatCoreHours(value.CoreSeconds), AllocationCount: value.AllocationCount}
	}
	return result
}

func formatCoreHours(coreSeconds int64) string {
	return fmt.Sprintf("%.2f", float64(coreSeconds)/3600)
}

type nodesView struct {
	appChrome
	Module         module
	Labels         detailCopy
	Nodes          []cluster.Node
	NodeSummary    nodeSummaryView
	NodesAvailable bool
	Success        string
	Error          string
}

type nodeSummaryView struct {
	Total   int
	Online  int
	Offline int
}

func newNodeSummary(nodes []cluster.Node) nodeSummaryView {
	summary := nodeSummaryView{Total: len(nodes)}
	for _, node := range nodes {
		if node.Online {
			summary.Online++
		} else {
			summary.Offline++
		}
	}
	return summary
}

type partitionNodeView struct {
	Name      string
	Partition string
	State     string
	Selected  bool
}

type partitionSystemRowView struct {
	Name       string
	Nodes      string
	ImportedAt string
}

type partitionManagedRowView struct {
	Name         string
	Nodes        string
	Status       string
	AddedNodes   string
	RemovedNodes string
	UpdatedAt    string
}

type partitionsView struct {
	appChrome
	Module              module
	Labels              partitionCopy
	Nodes               []partitionNodeView
	SystemPartitions    []partitionSystemRowView
	PlatformPartitions  []partitionManagedRowView
	NodesAvailable      bool
	PartitionsAvailable bool
	Error               string
	Success             string
	SelectedName        string
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

type ldapCopy struct {
	Description, Refresh, Search, SearchPlaceholder string
	Users, Groups, UID, Name, Email, UIDNumber      string
	GIDNumber, HomeDirectory, LoginShell            string
	DescriptionLabel, Members, Details, Back        string
	EmptyUsers, EmptyGroups, Unavailable, Truncated string
	UserDetails, GroupDetails, MembersTruncated     string
}

type ldapView struct {
	appChrome
	Module        module
	Labels        ldapCopy
	Page          directory.Page
	Users         []ldapUserRow
	Groups        []ldapGroupRow
	Query         string
	LDAPAvailable bool
}

type ldapUserRow struct {
	directory.User
	Key string
}

type ldapGroupRow struct {
	directory.Group
	Key string
}

type ldapUserView struct {
	appChrome
	Module        module
	Labels        ldapCopy
	User          directory.User
	LDAPAvailable bool
}

type ldapGroupView struct {
	appChrome
	Module        module
	Labels        ldapCopy
	Group         directory.Group
	LDAPAvailable bool
}

func ldapCopyFor(language string) ldapCopy {
	if language == "en" {
		return ldapCopy{
			Description: "Read-only RFC2307 identity directory", Refresh: "Refresh", Search: "Search directory", SearchPlaceholder: "UID, name, email or group",
			Users: "Directory users", Groups: "Directory groups", UID: "UID", Name: "Name", Email: "Email", UIDNumber: "UID number", GIDNumber: "GID number",
			HomeDirectory: "Home directory", LoginShell: "Login shell", DescriptionLabel: "Description", Members: "Members", Details: "Details", Back: "Back to LDAP directory",
			EmptyUsers: "No directory users", EmptyGroups: "No directory groups", Unavailable: "LDAP directory is temporarily unavailable", Truncated: "Results were limited; narrow the search",
			UserDetails: "User details", GroupDetails: "Group details", MembersTruncated: "Member list was limited",
		}
	}
	return ldapCopy{
		Description: "只读 RFC2307 身份目录", Refresh: "刷新", Search: "搜索目录", SearchPlaceholder: "UID、姓名、邮箱或组名",
		Users: "目录用户", Groups: "目录组", UID: "UID", Name: "姓名", Email: "邮箱", UIDNumber: "UID 编号", GIDNumber: "GID 编号",
		HomeDirectory: "主目录", LoginShell: "登录 Shell", DescriptionLabel: "描述", Members: "成员", Details: "详情", Back: "返回 LDAP 目录",
		EmptyUsers: "暂无目录用户", EmptyGroups: "暂无目录组", Unavailable: "LDAP 目录暂不可用", Truncated: "结果已截断，请缩小搜索范围",
		UserDetails: "用户详情", GroupDetails: "组详情", MembersTruncated: "成员列表已截断",
	}
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
			EmptyPartitions: "No partitions reported", PartitionStatus: "Partition status", NodeStatus: "Node status", TotalNodes: "Total nodes", OfflineNodes: "Unavailable nodes",
			Node: "Node", Partition: "Partition", State: "State", CPUs: "CPUs", Memory: "Memory", GRES: "GRES", Availability: "Availability",
			OnlineNodes: "Online nodes", CPUUtilization: "CPU utilization",
			JobID: "Job ID", JobName: "Name", User: "User", Account: "Account", Elapsed: "Elapsed", TimeLimit: "Time limit",
			NodesOrReason: "Nodes / reason", Online: "Online", Offline: "Unavailable", NodeCount: "Node count",
			BringNodeOnline: "Bring online", TakeNodeOffline: "Take offline",
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
		EmptyPartitions: "Slurm 未报告分区", PartitionStatus: "分区状态", NodeStatus: "节点状态", TotalNodes: "节点总数", OfflineNodes: "不可用节点",
		Node: "节点", Partition: "分区", State: "状态", CPUs: "CPU", Memory: "内存", GRES: "GRES", Availability: "可用性",
		OnlineNodes: "在线节点", CPUUtilization: "CPU 利用率",
		JobID: "作业 ID", JobName: "名称", User: "用户", Account: "账户", Elapsed: "已运行", TimeLimit: "时间限制",
		NodesOrReason: "节点 / 原因", Online: "在线", Offline: "不可用", NodeCount: "节点数",
		BringNodeOnline: "上线", TakeNodeOffline: "下线",
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

func partitionCopyFor(language string) partitionCopy {
	if language == "en" {
		return partitionCopy{
			Description: "Create or patch partitions by selecting live nodes. Saved definitions are kept in SQLite for fast recovery.",
			Refresh:     "Refresh", EmptyNodes: "No nodes reported", EmptyPartitions: "No saved partitions",
			Unavailable: "Partition data is temporarily unavailable", PartitionName: "Partition name", SelectedNodes: "Selected nodes",
			CreatePartition: "Create partition", UpdatePartition: "Patch partition",
			LiveNodes: "Live nodes", SystemPartitions: "System partitions", PlatformPartitions: "Platform partitions",
			Status: "Status", ReadOnly: "Read-only", Source: "Source", ImportedAt: "Imported at",
			EditPartition: "Edit partition", DeletePartition: "Delete partition", SavePartition: "Save partition",
			Matched: "Matched", Added: "Added", Removed: "Removed",
			Created: "Created", Patched: "Patched", Unchanged: "Unchanged", Deleted: "Deleted",
			Node: "Node", Partition: "Partition", State: "State", Action: "Action",
		}
	}
	return partitionCopy{
		Description: "通过勾选在线节点创建或补丁更新分区。已保存定义写入 SQLite，便于 Slurm 重启后快速恢复。",
		Refresh:     "刷新", EmptyNodes: "未报告节点", EmptyPartitions: "暂无已保存分区",
		Unavailable: "分区数据暂不可用", PartitionName: "分区名称", SelectedNodes: "已选节点",
		CreatePartition: "创建分区", UpdatePartition: "补丁更新",
		LiveNodes: "在线节点", SystemPartitions: "系统分区", PlatformPartitions: "平台分区",
		Status: "状态", ReadOnly: "只读", Source: "来源", ImportedAt: "导入时间",
		EditPartition: "编辑分区", DeletePartition: "删除分区", SavePartition: "保存分区",
		Matched: "一致", Added: "新增", Removed: "移除",
		Created: "已创建", Patched: "已更新", Unchanged: "无变化", Deleted: "已删除",
		Node: "节点", Partition: "分区", State: "状态", Action: "操作",
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
		{Path: "/slurm/nodes", Label: "节点管理", Group: "Slurm", Icon: "server"},
		{Path: "/slurm/partitions", Label: "分区管理", Group: "Slurm", Icon: "server"},
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
	labels := []string{"Overview", "Node management", "Partition management", "Jobs", "Accounts & users", "QoS & core hours", "LDAP directory", "Platform users", "Slurm configuration", "Files", "Terminal", "Audit log"}
	groups := []string{"Workspace", "Slurm", "Slurm", "Slurm", "Slurm", "Slurm", "Identity", "Identity", "System", "System", "System", "System"}
	result := make([]module, len(zh))
	for index, item := range zh {
		result[index] = module{Path: item.Path, Label: labels[index], Group: groups[index], Icon: item.Icon}
	}
	return result
}

func modulesForRole(language string, role platform.UserRole) []module {
	all := modulesFor(language)
	if role == platform.RoleAdmin {
		return all
	}
	allowed := map[string]bool{"/dashboard": true, "/slurm/jobs": true, "/system/files": true, "/terminal": true}
	filtered := make([]module, 0, len(allowed))
	for _, item := range all {
		if allowed[item.Path] {
			filtered = append(filtered, item)
		}
	}
	return filtered
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
