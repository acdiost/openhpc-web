package web

import (
	"context"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/acdiost/openhpc-web/internal/cluster"
	"github.com/acdiost/openhpc-web/internal/platform"
	"github.com/labstack/echo/v4"
)

func (a *application) slurmPartitions(c echo.Context) error {
	lang := language(c)
	labels := partitionCopyFor(lang)
	module := moduleByPath("/slurm/partitions", lang)

	liveNodes, nodesAvailable := a.loadPartitionNodes(c)
	systemPartitions, managedPartitions, partitionsAvailable := a.loadPartitionRecords(c)
	selectedName := strings.TrimSpace(c.QueryParam("name"))
	selectedSpec, selectedManaged := partitionSelection(systemPartitions, managedPartitions, selectedName)
	success := partitionSuccessFor(lang, c.QueryParam("saved"), selectedName)
	errText := partitionErrorFor(lang, c.QueryParam("error"), selectedName)
	openEditor := c.QueryParam("modal") == "create" || (selectedManaged && c.QueryParam("saved") == "")
	view := partitionsView{
		appChrome: a.newAppChrome(c, module.Path, nodesAvailable || partitionsAvailable, pageHeading{
			Eyebrow: "OPENHPC / SLURM", Title: module.Label, Description: labels.Description,
			RefreshPath: module.Path, RefreshLabel: labels.Refresh,
		}),
		Module: module, Labels: labels,
		Nodes:              partitionNodesView(liveNodes, selectedSpec.Nodes),
		SystemPartitions:   partitionSystemRowsView(systemPartitions, labels),
		PlatformPartitions: partitionManagedRowsView(managedPartitions, liveNodes, nodesAvailable, labels),
		NodesAvailable:     nodesAvailable, PartitionsAvailable: partitionsAvailable,
		Success: success, Error: errText, SelectedName: selectedName, OpenEditor: openEditor,
	}
	if !selectedManaged {
		view.SelectedName = ""
		view.Nodes = partitionNodesView(liveNodes, nil)
	}
	return a.render(c, http.StatusOK, "partitions.html", view)
}

func (a *application) savePartition(c echo.Context) error {
	if a.partitionStore == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable)
	}
	if err := c.Request().ParseForm(); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest)
	}
	name := strings.TrimSpace(c.FormValue("name"))
	selected := append([]string(nil), c.Request().PostForm["nodes"]...)
	liveNodes, ok := a.partitionNodeNames(c)
	if !ok {
		return echo.NewHTTPError(http.StatusServiceUnavailable)
	}
	if name == "" {
		return echo.NewHTTPError(http.StatusBadRequest)
	}
	if len(selected) == 0 {
		return echo.NewHTTPError(http.StatusBadRequest)
	}
	for _, node := range selected {
		if !liveNodes[node] {
			return echo.NewHTTPError(http.StatusBadRequest)
		}
	}
	change, err := a.partitionStore.Upsert(c.Request().Context(), platform.PartitionSpec{Name: name, Nodes: selected, Managed: true})
	if err != nil {
		_ = a.recordAudit(c, platform.AuditEvent{Actor: currentPrincipal(c).Username, Action: "slurm.partition.save", Outcome: "failed", CreatedAt: time.Now()})
		code := "save_failed"
		if strings.Contains(err.Error(), "read-only") {
			code = "readonly"
		}
		return c.Redirect(http.StatusSeeOther, "/slurm/partitions?error="+url.QueryEscape(partitionErrorFor(language(c), code, name))+"&name="+url.QueryEscape(name))
	}
	if a.partitionAdmin != nil {
		if err := a.partitionAdmin.ApplyPartition(c.Request().Context(), name, selected); err != nil {
			_ = a.recordAudit(c, platform.AuditEvent{Actor: currentPrincipal(c).Username, Action: "slurm.partition.sync", Outcome: "failed", CreatedAt: time.Now()})
			return c.Redirect(http.StatusSeeOther, "/slurm/partitions?error="+url.QueryEscape(partitionErrorFor(language(c), "sync_failed", name))+"&name="+url.QueryEscape(name))
		}
	}
	outcome := "success"
	saved := "updated"
	if change.Created {
		saved = "created"
	} else if !change.Updated {
		saved = "unchanged"
	}
	if err := a.recordAudit(c, platform.AuditEvent{Actor: currentPrincipal(c).Username, Action: "slurm.partition.save", Outcome: outcome, CreatedAt: time.Now()}); err != nil {
		log.Printf("partition audit failed")
	}
	return c.Redirect(http.StatusSeeOther, "/slurm/partitions?saved="+saved+"&name="+url.QueryEscape(name))
}

func (a *application) deletePartition(c echo.Context) error {
	if a.partitionStore == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable)
	}
	if err := c.Request().ParseForm(); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest)
	}
	name := strings.TrimSpace(c.FormValue("name"))
	if name == "" {
		return echo.NewHTTPError(http.StatusBadRequest)
	}
	spec, found, err := a.partitionStore.Get(c.Request().Context(), name)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest)
	}
	if !found {
		return c.Redirect(http.StatusSeeOther, "/slurm/partitions?saved=deleted")
	}
	if !spec.Managed {
		return c.Redirect(http.StatusSeeOther, "/slurm/partitions?error="+url.QueryEscape(partitionErrorFor(language(c), "readonly", name))+"&name="+url.QueryEscape(name))
	}
	if err := a.partitionStore.DeleteManaged(c.Request().Context(), name); err != nil {
		_ = a.recordAudit(c, platform.AuditEvent{Actor: currentPrincipal(c).Username, Action: "slurm.partition.delete", Outcome: "failed", CreatedAt: time.Now()})
		return c.Redirect(http.StatusSeeOther, "/slurm/partitions?error="+url.QueryEscape(partitionErrorFor(language(c), "delete_failed", name))+"&name="+url.QueryEscape(name))
	}
	if a.partitionAdmin != nil {
		if err := a.partitionAdmin.DeletePartition(c.Request().Context(), name); err != nil {
			_, _ = a.partitionStore.Upsert(c.Request().Context(), spec)
			_ = a.recordAudit(c, platform.AuditEvent{Actor: currentPrincipal(c).Username, Action: "slurm.partition.sync", Outcome: "failed", CreatedAt: time.Now()})
			return c.Redirect(http.StatusSeeOther, "/slurm/partitions?error="+url.QueryEscape(partitionErrorFor(language(c), "sync_failed", name))+"&name="+url.QueryEscape(name))
		}
	}
	if err := a.recordAudit(c, platform.AuditEvent{Actor: currentPrincipal(c).Username, Action: "slurm.partition.delete", Outcome: "success", CreatedAt: time.Now()}); err != nil {
		log.Printf("partition audit failed")
	}
	return c.Redirect(http.StatusSeeOther, "/slurm/partitions?saved=deleted")
}

func (a *application) syncSystemPartitions(ctx context.Context) error {
	if a.partitionStore == nil || a.nodeProvider == nil {
		return nil
	}
	nodes, err := a.nodeProvider.Nodes(ctx)
	if err != nil {
		return err
	}
	partitions := partitionSpecsFromNodes(nodes)
	for _, partition := range partitions {
		if _, err := a.partitionStore.ImportSystem(ctx, partition); err != nil {
			return err
		}
	}
	return nil
}

func (a *application) syncStoredPartitions(ctx context.Context) error {
	if a.partitionStore == nil || a.partitionAdmin == nil {
		return nil
	}
	partitions, err := a.partitionStore.ListManaged(ctx)
	if err != nil {
		return err
	}
	for _, partition := range partitions {
		if err := a.partitionAdmin.ApplyPartition(ctx, partition.Name, partition.Nodes); err != nil {
			return err
		}
	}
	return nil
}

func (a *application) loadPartitionNodes(c echo.Context) ([]cluster.Node, bool) {
	if a.nodeProvider == nil {
		return nil, false
	}
	nodes, err := a.nodeProvider.Nodes(c.Request().Context())
	if err != nil {
		log.Printf("Slurm nodes snapshot failed: %v", err)
		return nil, false
	}
	return nodes, true
}

func (a *application) loadPartitionRecords(c echo.Context) ([]platform.PartitionSpec, []platform.PartitionSpec, bool) {
	if a.partitionStore == nil {
		return nil, nil, false
	}
	systemPartitions, err := a.partitionStore.ListSystem(c.Request().Context())
	if err != nil {
		log.Printf("partition store query failed")
		return nil, nil, false
	}
	managedPartitions, err := a.partitionStore.ListManaged(c.Request().Context())
	if err != nil {
		log.Printf("partition store query failed")
		return nil, nil, false
	}
	return systemPartitions, managedPartitions, true
}

func (a *application) partitionNodeNames(c echo.Context) (map[string]bool, bool) {
	nodes, ok := a.loadPartitionNodes(c)
	if !ok {
		return nil, false
	}
	allowed := make(map[string]bool, len(nodes))
	for _, node := range nodes {
		allowed[node.Name] = true
	}
	return allowed, true
}

func partitionSelection(systemPartitions, managedPartitions []platform.PartitionSpec, selectedName string) (platform.PartitionSpec, bool) {
	for _, partition := range managedPartitions {
		if partition.Name == selectedName {
			return partition, true
		}
	}
	for _, partition := range systemPartitions {
		if partition.Name == selectedName {
			return partition, false
		}
	}
	return platform.PartitionSpec{}, false
}

func partitionNodesView(nodes []cluster.Node, selected []string) []partitionNodeView {
	selectedSet := make(map[string]struct{}, len(selected))
	for _, node := range selected {
		selectedSet[node] = struct{}{}
	}
	views := make([]partitionNodeView, 0, len(nodes))
	for _, node := range nodes {
		_, checked := selectedSet[node.Name]
		views = append(views, partitionNodeView{Name: node.Name, Partition: node.Partition, State: node.State, Selected: checked})
	}
	return views
}

func partitionSystemRowsView(partitions []platform.PartitionSpec, labels partitionCopy) []partitionSystemRowView {
	if len(partitions) == 0 {
		return nil
	}
	rows := make([]partitionSystemRowView, 0, len(partitions))
	for _, partition := range partitions {
		rows = append(rows, partitionSystemRowView{
			Name:       partition.Name,
			Nodes:      strings.Join(partition.Nodes, ", "),
			ImportedAt: partition.UpdatedAt.Local().Format("2006-01-02 15:04"),
		})
	}
	return rows
}

func partitionManagedRowsView(partitions []platform.PartitionSpec, liveNodes []cluster.Node, nodesAvailable bool, labels partitionCopy) []partitionManagedRowView {
	if len(partitions) == 0 {
		return nil
	}
	liveByPartition := make(map[string][]string)
	for _, node := range liveNodes {
		liveByPartition[node.Partition] = append(liveByPartition[node.Partition], node.Name)
	}
	rows := make([]partitionManagedRowView, 0, len(partitions))
	for _, partition := range partitions {
		liveNodesForPartition := liveByPartition[partition.Name]
		status := labels.Unavailable
		added, removed := []string(nil), []string(nil)
		if nodesAvailable {
			added, removed = diffPartitionNodes(partition.Nodes, liveNodesForPartition)
			status = labels.Matched
			if len(added) > 0 || len(removed) > 0 {
				status = labels.Patched
			}
		}
		rows = append(rows, partitionManagedRowView{
			Name:         partition.Name,
			Nodes:        strings.Join(partition.Nodes, ", "),
			Status:       status,
			AddedNodes:   strings.Join(added, ", "),
			RemovedNodes: strings.Join(removed, ", "),
			UpdatedAt:    partition.UpdatedAt.Local().Format("2006-01-02 15:04"),
		})
	}
	return rows
}

func partitionSpecsFromNodes(nodes []cluster.Node) []platform.PartitionSpec {
	byPartition := make(map[string][]string)
	for _, node := range nodes {
		if strings.TrimSpace(node.Partition) == "" {
			continue
		}
		byPartition[node.Partition] = append(byPartition[node.Partition], node.Name)
	}
	partitions := make([]platform.PartitionSpec, 0, len(byPartition))
	for name, nodeNames := range byPartition {
		partitions = append(partitions, platform.PartitionSpec{Name: name, Nodes: nodeNames, Managed: false})
	}
	sort.Slice(partitions, func(i, j int) bool { return partitions[i].Name < partitions[j].Name })
	return partitions
}

func diffPartitionNodes(existing, desired []string) ([]string, []string) {
	existingSet := make(map[string]struct{}, len(existing))
	for _, node := range existing {
		existingSet[node] = struct{}{}
	}
	desiredSet := make(map[string]struct{}, len(desired))
	for _, node := range desired {
		desiredSet[node] = struct{}{}
	}
	added := make([]string, 0)
	for _, node := range desired {
		if _, found := existingSet[node]; !found {
			added = append(added, node)
		}
	}
	removed := make([]string, 0)
	for _, node := range existing {
		if _, found := desiredSet[node]; !found {
			removed = append(removed, node)
		}
	}
	return added, removed
}

func partitionSuccessFor(language, saved, name string) string {
	if saved == "deleted" {
		if language == "en" {
			return "Partition deleted."
		}
		return "分区已删除。"
	}
	if saved == "" || name == "" {
		return ""
	}
	switch saved {
	case "created":
		if language == "en" {
			return "Partition " + name + " created."
		}
		return "分区 " + name + " 已创建。"
	case "updated":
		if language == "en" {
			return "Partition " + name + " patched."
		}
		return "分区 " + name + " 已更新。"
	case "unchanged":
		if language == "en" {
			return "Partition " + name + " is unchanged."
		}
		return "分区 " + name + " 无变化。"
	default:
		return ""
	}
}

func partitionErrorFor(language, code, name string) string {
	if code == "" {
		return ""
	}
	switch code {
	case "readonly":
		if language == "en" {
			return "Partition " + name + " is read-only."
		}
		return "分区 " + name + " 为只读。"
	case "save_failed":
		if language == "en" {
			return "Partition " + name + " could not be saved."
		}
		return "分区 " + name + " 保存失败。"
	case "sync_failed":
		if language == "en" {
			return "Partition " + name + " could not be synced to Slurm."
		}
		return "分区 " + name + " 无法同步到 Slurm。"
	case "delete_failed":
		if language == "en" {
			return "Partition " + name + " could not be deleted."
		}
		return "分区 " + name + " 删除失败。"
	default:
		return ""
	}
}
