package web

import (
	"log"
	"net/http"
	"net/url"
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
	storedPartitions, partitionsSaved := a.loadSavedPartitions(c)
	selectedName := strings.TrimSpace(c.QueryParam("name"))
	var selectedSpec platform.PartitionSpec
	if selectedName != "" && a.partitionStore != nil {
		spec, found, err := a.partitionStore.Get(c.Request().Context(), selectedName)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest)
		}
		if found {
			selectedSpec = spec
		}
	}
	success := partitionSuccessFor(lang, c.QueryParam("saved"), selectedName)
	views := partitionNodesView(liveNodes, selectedSpec.Nodes)
	rows := partitionRowsView(storedPartitions, liveNodes, nodesAvailable, labels)
	view := partitionsView{
		appChrome: a.newAppChrome(c, module.Path, nodesAvailable || partitionsSaved, pageHeading{
			Eyebrow: "OPENHPC / SLURM", Title: module.Label, Description: labels.Description,
			RefreshPath: module.Path, RefreshLabel: labels.Refresh,
		}),
		Module: module, Labels: labels, Nodes: views, Partitions: rows,
		NodesAvailable: nodesAvailable, PartitionsSaved: partitionsSaved,
		Success: success, SelectedName: selectedName,
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
	change, err := a.partitionStore.Upsert(c.Request().Context(), platform.PartitionSpec{Name: name, Nodes: selected})
	if err != nil {
		_ = a.recordAudit(c, platform.AuditEvent{Actor: currentPrincipal(c).Username, Action: "slurm.partition.save", Outcome: "failed", CreatedAt: time.Now()})
		return echo.NewHTTPError(http.StatusServiceUnavailable)
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

func (a *application) loadSavedPartitions(c echo.Context) ([]platform.PartitionSpec, bool) {
	if a.partitionStore == nil {
		return nil, false
	}
	partitions, err := a.partitionStore.List(c.Request().Context())
	if err != nil {
		log.Printf("partition store query failed")
		return nil, false
	}
	return partitions, true
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

func partitionRowsView(partitions []platform.PartitionSpec, liveNodes []cluster.Node, nodesAvailable bool, labels partitionCopy) []partitionRowView {
	if len(partitions) == 0 {
		return nil
	}
	liveByPartition := make(map[string][]string)
	for _, node := range liveNodes {
		liveByPartition[node.Partition] = append(liveByPartition[node.Partition], node.Name)
	}
	rows := make([]partitionRowView, 0, len(partitions))
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
		rows = append(rows, partitionRowView{
			Name:         partition.Name,
			Nodes:        strings.Join(partition.Nodes, ", "),
			Status:       status,
			AddedNodes:   strings.Join(added, ", "),
			RemovedNodes: strings.Join(removed, ", "),
		})
	}
	return rows
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
		return "分区 " + name + " 已补丁更新。"
	case "unchanged":
		if language == "en" {
			return "Partition " + name + " is unchanged."
		}
		return "分区 " + name + " 无变化。"
	default:
		return ""
	}
}
