package kaas

import (
	"encoding/json"
	"fmt"
	"log"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// WSMessage represents websocket message format
type WSMessage struct {
	Message string            `json:"message"`
	Action  string            `json:"action"`
	Data    map[string]string `json:"data,omitempty"`
}

var wsupgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
}

const kubeConfigTemplate = `apiVersion: v1
clusters:
- cluster:
    server: %s
  name: static-kas
contexts:
- context:
    cluster: static-kas
    namespace: default
  name: static-kas
current-context: static-kas
kind: Config`

func sendWSMessage(conn *websocket.Conn, action string, message string) {
	response := WSMessage{
		Action:  action,
		Message: message,
	}
	responseJSON, err := json.Marshal(response)
	if err != nil {
		fmt.Println("Can't serialize", response)
	}
	if conn != nil {
		conn.WriteMessage(websocket.TextMessage, responseJSON)
	}
}

func sendWSMessageWithData(conn *websocket.Conn, action string, message string, data map[string]string) {
	response := WSMessage{
		Action:  action,
		Message: message,
		Data:    data,
	}
	responseJSON, err := json.Marshal(response)
	if err != nil {
		fmt.Println("Can't serialize", response)
	}
	if conn != nil {
		conn.WriteMessage(websocket.TextMessage, responseJSON)
	}
}

// HandleStatusViaWS reads websocket events and runs actions
func (s *ServerSettings) HandleStatusViaWS(c *gin.Context) {
	conn, err := wsupgrader.Upgrade(c.Writer, c.Request, nil)

	if err != nil {
		log.Printf("Failed to upgrade ws: %+v", err)
		return
	}

	for {
		t, msg, err := conn.ReadMessage()
		log.Printf("Got ws message: %s", msg)
		if err != nil {
			if !websocket.IsCloseError(err, 1001, 1006) {
				delete(s.Conns, conn.RemoteAddr().String())
				log.Printf("Error reading message: %+v", err)
			}
			break
		}
		if t != websocket.TextMessage {
			log.Printf("Not a text message: %d", t)
			continue
		}
		var m WSMessage
		err = json.Unmarshal(msg, &m)
		if err != nil {
			log.Printf("Failed to unmarshal message '%+v': %+v", string(msg), err)
			continue
		}
		log.Printf("WS message: %+v", m)
		switch m.Action {
		case "connect":
			s.Conns[conn.RemoteAddr().String()] = conn
			go s.sendResourceQuotaUpdate()
		case "new":
			go s.newKAS(conn, m.Message)
		case "delete":
			go s.removeKAS(conn, m.Message)
		}
	}
}

func (s *ServerSettings) sendResourceQuotaUpdate() {
	rqsJSON, err := json.Marshal(s.RQStatus)
	if err != nil {
		log.Fatalf("Can't serialize %s", err)
	}
	for _, conn := range s.Conns {
		sendWSMessage(conn, "rquota", string(rqsJSON))
	}
}

func (s *ServerSettings) removeKAS(conn *websocket.Conn, appName string) {
	sendWSMessage(conn, "status", fmt.Sprintf("Removing app %s", appName))
	if output, err := s.deletePods(appName); err != nil {
		sendWSMessage(conn, "failure", fmt.Sprintf("%s\n%s", output, err.Error()))
		return
	}
	delete(s.Datasources, appName)
	sendWSMessage(conn, "done", "KAS instance removed")
}

func (s *ServerSettings) newKAS(conn *websocket.Conn, rawURL string) {
	// Generate a unique app label
	appLabel := generateAppLabel()
	sendWSMessage(conn, "app-label", appLabel)

	// Fetch must-gather.tar path if prow URL specified
	prowInfo, err := getTarPaths(conn, rawURL)
	if err != nil {
		sendWSMessage(conn, "failure", fmt.Sprintf("Failed to find must-gather archive: %s", err.Error()))
		return
	}

	if len(prowInfo.ClusterDumpURLs) == 0 {
		sendWSMessage(conn, "failure", "No dump tarballs found")
		return
	}

	if len(prowInfo.ClusterDumpURLs) > 1 {
		data, err := json.Marshal(prowInfo.ClusterDumpURLs)
		if err != nil {
			sendWSMessage(conn, "failure", fmt.Sprintf("Failed to marshal dump configs: +%v", err))
		}
		sendWSMessage(conn, "choose", string(data))
		return
	}

	dumpURL := prowInfo.ClusterDumpURLs[0]

	// Create a new app in the namespace and return route
	sendWSMessage(conn, "status", "Deploying a new KAS instance")

	var kasRoute string
	var consoleRoute string
	if kasRoute, consoleRoute, err = s.launchKASApp(appLabel, dumpURL); err != nil {
		sendWSMessage(conn, "failure", fmt.Sprintf("Failed to run a new app: %s", err.Error()))
		return
	}
	kubeconfig := fmt.Sprintf(kubeConfigTemplate, kasRoute)
	sendWSMessage(conn, "kubeconfig", kubeconfig)

	sendWSMessage(conn, "progress", "Waiting for pods to become ready")
	if err := s.waitForDeploymentReady(appLabel); err != nil {
		sendWSMessage(conn, "failure", err.Error())
		return
	}
	sendWSMessage(conn, "link", consoleRoute)

	data := map[string]string{
		"hash": appLabel,
		"url":  kasRoute,
	}
	sendWSMessageWithData(conn, "done", "Pod is ready", data)
}
