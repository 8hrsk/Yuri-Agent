package domain

type Actor string

const (
	ActorUser   Actor = "user"
	ActorAgent  Actor = "agent"
	ActorSystem Actor = "system"
	ActorPlugin Actor = "plugin"
)

func (a Actor) Valid() bool {
	switch a {
	case ActorUser, ActorAgent, ActorSystem, ActorPlugin:
		return true
	default:
		return false
	}
}
