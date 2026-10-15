package client

import (
	"fmt"
	"net/url"
)

func claimNodePath(runID, nodeID, verb string) string {
	return fmt.Sprintf("/api/v1/runs/%s/nodes/%s/%s", url.PathEscape(runID), url.PathEscape(nodeID), verb)
}

func childPath(runID, childID string) string {
	return "/api/v1/runs/" + url.PathEscape(runID) + "/children/" + url.PathEscape(childID)
}
