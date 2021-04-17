package api

import (
	"context"
	"fmt"
	"github.com/cloudhut/common/rest"
	"github.com/gorilla/websocket"
	"go.uber.org/zap"
	"net/http"
	"sync"
	"time"
)

type websocketClient struct {
	Ctx        context.Context
	Cancel     context.CancelFunc
	Logger     *zap.Logger
	Connection *websocket.Conn
	Mutex      *sync.RWMutex
}

func (wc *websocketClient) upgrade(w http.ResponseWriter, r *http.Request) *rest.Error {
	upgrader := websocket.Upgrader{
		EnableCompression: true,
		CheckOrigin:       func(r *http.Request) bool { return true },
	}

	// TODO: Add user information to logger?
	wc.Logger.Debug("starting websocket connection upgrade")
	wsConnection, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		restErr := &rest.Error{
			Err:      fmt.Errorf("failed to upgrade websocket in messages endpoint %w", err),
			Status:   http.StatusBadRequest,
			Message:  "Failed upgrade websocket",
			IsSilent: false,
		}
		return restErr
	}
	wc.Logger.Debug("websocket upgrade complete")

	maxMessageSize := int64(16 * 1024) // 16kb
	wsConnection.SetReadLimit(maxMessageSize)
	wsConnection.SetCloseHandler(wc.onClose)
	wsConnection.SetReadDeadline(time.Now().Add(10 * time.Second)) // Grant max 10s until readLoop() is setup
	wc.Connection = wsConnection

	return nil
}

// readLoop reads all messages received on the websocket connection so that it can handle pong and close messages
// sent by the websocket peer.
func (wc *websocketClient) readLoop() {
	maxSilence := time.Second * 10
	wc.Connection.SetPongHandler(func(string) error { wc.Connection.SetReadDeadline(time.Now().Add(maxSilence)); return nil })
	wc.Connection.SetReadDeadline(time.Now().Add(maxSilence))

	for {
		if _, _, err := wc.Connection.NextReader(); err != nil {
			wc.writeJSON(rest.Error{
				Status:  http.StatusRequestTimeout,
				Message: "Last received pong was too long ago",
			})
			wc.sendClose()
			break
		}
	}
}

// producePings sends ping messages until websocket connection is broken
func (wc *websocketClient) producePings() {
	ticker := time.NewTicker(time.Second * 3)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if err := wc.writeMessage(websocket.PingMessage, []byte{}); err != nil {
				return
			}
		}
	}
}

func (wc *websocketClient) readJSON(v interface{}) error {
	wc.Mutex.RLock()
	defer wc.Mutex.RUnlock()

	return wc.Connection.ReadJSON(v)
}

func (wc *websocketClient) writeJSON(v interface{}) error {
	wc.Mutex.Lock()
	defer wc.Mutex.Unlock()

	return wc.Connection.WriteJSON(v)
}

func (wc *websocketClient) writeMessage(messageType int, data []byte) error {
	wc.Mutex.Lock()
	defer wc.Mutex.Unlock()

	return wc.Connection.WriteMessage(messageType, data)
}

func (wc *websocketClient) sendClose() {
	wc.Cancel()

	// Close connection gracefully!
	err := wc.writeMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
	if err != nil && err != websocket.ErrCloseSent {
		wc.Logger.Debug("failed to send 'CloseNormalClosure' to ws connection", zap.Error(err))
	} else {
		// Wait for client close event for up to 2s
		time.Sleep(2 * time.Second)
	}
	_ = wc.Connection.Close()
}

func (wc *websocketClient) onClose(code int, text string) error {
	wc.Logger.Debug("connection has been closed by client", zap.Int("code", code), zap.String("text", text))
	wc.Cancel()
	return nil
}
