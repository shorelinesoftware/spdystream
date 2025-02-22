/*
   Copyright 2014-2021 Docker Inc.

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package ws

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/moby/spdystream"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
}

var serverSpdyConn *spdystream.Connection

// Connect to the Websocket endpoint at ws://localhost
// using SPDY over Websockets framing.
func ExampleConn() {
	wsconn, _, _ := websocket.DefaultDialer.Dial("ws://localhost/", http.Header{"Origin": {"http://localhost/"}})
	conn, _ := spdystream.NewConnection(NewConnection(wsconn), false)
	go conn.Serve(spdystream.NoOpStreamHandler)
	stream, _ := conn.CreateStream(http.Header{}, nil, false)
	stream.Wait()
}

func serveWs(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", 405)
		return
	}

	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		if _, ok := err.(websocket.HandshakeError); !ok {
			log.Println(err)
		}
		return
	}

	wrap := NewConnection(ws)
	spdyConn, err := spdystream.NewConnection(wrap, true)
	if err != nil {
		log.Fatal(err)
		return
	}
	serverSpdyConn = spdyConn
	spdyConn.Serve(authStreamHandler)
}

func TestSpdyStreamOverWs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(serveWs))
	defer server.Close()
	defer func() {
		if serverSpdyConn != nil {
			serverSpdyConn.Close()
		}
	}()

	wsconn, _, err := websocket.DefaultDialer.Dial(strings.Replace(server.URL, "http://", "ws://", 1), http.Header{"Origin": {server.URL}})
	if err != nil {
		t.Fatal(err)
	}

	wrap := NewConnection(wsconn)
	spdyConn, err := spdystream.NewConnection(wrap, false)
	if err != nil {
		defer wsconn.Close()
		t.Fatal(err)
	}
	defer spdyConn.Close()
	authenticated = true
	go spdyConn.Serve(spdystream.NoOpStreamHandler)

	stream, streamErr := spdyConn.CreateStream(http.Header{}, nil, false)
	if streamErr != nil {
		t.Fatalf("Error creating stream: %s", streamErr)
	}

	waitErr := stream.Wait()
	if waitErr != nil {
		t.Fatalf("Error waiting for stream: %s", waitErr)
	}

	message := []byte("hello")
	writeErr := stream.WriteData(message, false)
	if writeErr != nil {
		t.Fatalf("Error writing data")
	}

	buf := make([]byte, 10)
	n, readErr := stream.Read(buf)
	if readErr != nil {
		t.Fatalf("Error reading data from stream: %s", readErr)
	}
	if n != 5 {
		t.Fatalf("Unexpected number of bytes read:\nActual: %d\nExpected: 5", n)
	}
	if !bytes.Equal(buf[:n], message) {
		t.Fatalf("Did not receive expected message:\nActual: %s\nExpectd: %s", buf, message)
	}

	writeErr = stream.WriteData(message, true)
	if writeErr != nil {
		t.Fatalf("Error writing data")
	}

	smallBuf := make([]byte, 3)
	n, readErr = stream.Read(smallBuf)
	if readErr != nil {
		t.Fatalf("Error reading data from stream: %s", readErr)
	}
	if n != 3 {
		t.Fatalf("Unexpected number of bytes read:\nActual: %d\nExpected: 3", n)
	}
	if !bytes.Equal(smallBuf[:n], []byte("hel")) {
		t.Fatalf("Did not receive expected message:\nActual: %s\nExpectd: %s", smallBuf[:n], message)
	}
	n, readErr = stream.Read(smallBuf)
	if readErr != nil {
		t.Fatalf("Error reading data from stream: %s", readErr)
	}
	if n != 2 {
		t.Fatalf("Unexpected number of bytes read:\nActual: %d\nExpected: 2", n)
	}
	if !bytes.Equal(smallBuf[:n], []byte("lo")) {
		t.Fatalf("Did not receive expected message:\nActual: %s\nExpected: lo", smallBuf[:n])
	}

	n, readErr = stream.Read(buf)
	if readErr != io.EOF {
		t.Fatalf("Expected EOF reading from finished stream, read %d bytes", n)
	}

	// Closing again should return error since the stream is already closed
	streamCloseErr := stream.Close()
	if streamCloseErr == nil {
		t.Fatalf("No error closing finished stream")
	}
	if streamCloseErr != spdystream.ErrWriteClosedStream {
		t.Fatalf("Unexpected error closing stream: %s", streamCloseErr)
	}

	streamResetErr := stream.Reset()
	if streamResetErr != nil {
		t.Fatalf("Error reseting stream: %s", streamResetErr)
	}

	authenticated = false
	badStream, badStreamErr := spdyConn.CreateStream(http.Header{}, nil, false)
	if badStreamErr != nil {
		t.Fatalf("Error creating stream: %s", badStreamErr)
	}

	waitErr = badStream.Wait()
	if waitErr == nil {
		t.Fatalf("Did not receive error creating stream")
	}
	if waitErr != spdystream.ErrReset {
		t.Fatalf("Unexpected error creating stream: %s", waitErr)
	}

	spdyCloseErr := spdyConn.Close()
	if spdyCloseErr != nil {
		t.Fatalf("Error closing spdy connection: %s", spdyCloseErr)
	}
}

var authenticated bool

func authStreamHandler(stream *spdystream.Stream) {
	if !authenticated {
		stream.Refuse()
		return
	}
	spdystream.MirrorStreamHandler(stream)
}
