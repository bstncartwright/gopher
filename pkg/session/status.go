package session

func applyStatusTransition(current SessionStatus, event Event) SessionStatus {
	switch event.Type {
	case EventControl:
		control, ok := controlFromPayload(event.Payload)
		if !ok {
			return current
		}
		switch control.Action {
		case ControlActionSessionCreated:
			return SessionActive
		case ControlActionSessionCancelled:
			return SessionPaused
		case ControlActionSessionCompleted:
			return SessionCompleted
		case ControlActionSessionFailed:
			return SessionFailed
		default:
			return current
		}
	default:
		return current
	}
}
