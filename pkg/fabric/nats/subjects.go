package nats

import "fmt"

func SessionEventsSubject(sessionID string) string {
	return fmt.Sprintf("session.%s.events", sessionID)
}

func AgentInboxSubject(agentID string) string {
	return fmt.Sprintf("agent.%s.inbox", agentID)
}

func NodeStatusSubject(nodeID string) string {
	return fmt.Sprintf("node.%s.status", nodeID)
}

func NodeCapabilitiesSubject(nodeID string) string {
	return fmt.Sprintf("node.%s.capabilities", nodeID)
}

func NodeControlSubject(nodeID string) string {
	return fmt.Sprintf("node.%s.control", nodeID)
}

func CapabilityInvokeSubject(capability string) string {
	return fmt.Sprintf("cap.%s.invoke", capability)
}
