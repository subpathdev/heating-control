package mqtt

import "context"

// connection is the part of the MQTT client the health check needs.
type connection interface {
	IsConnectionOpen() bool
}

type connectionChecker struct {
	conn connection
}

func (c connectionChecker) Name() string { return "mqtt" }

func (c connectionChecker) Check(context.Context) error {
	if !c.conn.IsConnectionOpen() {
		return errNotConnected
	}
	return nil
}
