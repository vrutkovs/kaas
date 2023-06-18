package kaas

import (
	"github.com/gorilla/websocket"
	routeClient "github.com/openshift/client-go/route/clientset/versioned/typed/route/v1"
	k8s "k8s.io/client-go/kubernetes"
)

// RQuotaStatus stores ResourceQuota info
type RQuotaStatus struct {
	Used int64 `json:"used"`
	Hard int64 `json:"hard"`
}

// ServerSettings stores info about the server
type ServerSettings struct {
	K8sClient   *k8s.Clientset
	RouteClient *routeClient.RouteV1Client
	Namespace   string
	RQuotaName  string
	RQStatus    *RQuotaStatus
	Conns       map[string]*websocket.Conn
	Datasources map[string]int
}

// ProwJSON stores test start / finished timestamp
type ProwJSON struct {
	Timestamp int `json:"timestamp"`
}

// ProwInfo stores all links and data collected via scanning for must gather
type ProwInfo struct {
	ClusterDumpURLs []string
}
