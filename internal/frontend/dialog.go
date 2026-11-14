package frontend

import (
	"context"
	"encoding/json"

	"rumbo/protocol"
)

// talk is the Dialog of a function the front end called. What it says
// reaches the front end as text events naming the invoke, and what it asks
// as an ask request. Neither the questions nor the answers are recorded.
type talk struct {
	conn   *Conn
	invoke int64
}

func (t talk) Say(text string) {
	t.conn.event(protocol.Event{Type: protocol.EventText, Invoke: t.invoke, Text: text})
}

func (t talk) Ask(ctx context.Context, question string, secret bool) (string, error) {
	return t.conn.ask(ctx, protocol.FrontendAskParams{Invoke: t.invoke, Text: question, Secret: secret})
}

// ask asks the front end a question for the person, and waits for the
// answer until ctx ends.
func (c *Conn) ask(ctx context.Context, params protocol.FrontendAskParams) (string, error) {
	answer := make(chan protocol.Frame, 1)

	c.running.Lock()
	c.nextID++
	id := c.nextID
	c.asked[id] = answer
	c.running.Unlock()

	defer func() {
		c.running.Lock()
		delete(c.asked, id)
		c.running.Unlock()
	}()

	raw, _ := json.Marshal(params)

	if err := c.send(protocol.Frame{ID: &id, Method: protocol.MethodAsk, Params: raw}); err != nil {
		return "", err
	}

	select {
	case <-ctx.Done():
		return "", ctx.Err()

	case frame := <-answer:
		if frame.Error != nil {
			return "", frame.Error
		}

		var result protocol.AskResult

		if err := json.Unmarshal(frame.Result, &result); err != nil {
			return "", err
		}

		return result.Text, nil
	}
}

// answered passes the front end's answer to the question it belongs to.
func (c *Conn) answered(frame protocol.Frame) {
	c.running.Lock()
	answer := c.asked[*frame.ID]
	delete(c.asked, *frame.ID)
	c.running.Unlock()

	if answer != nil {
		answer <- frame
	}
}
