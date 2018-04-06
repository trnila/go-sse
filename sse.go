package sse

import (
    "fmt"
    "log"
    "net/http"
)

type Server struct {
    options *Options
    channels map[string]*Channel
    addClient chan *Client
    removeClient chan *Client
    shutdown chan bool
    closeChannel chan string
    Debug bool
}

// NewServer creates a new SSE server.
func NewServer(options *Options) *Server {
    if options == nil {
        options = &Options{}
    }

    s := &Server{
        options,
        make(map[string]*Channel),
        make(chan *Client),
        make(chan *Client),
        make(chan bool),
        make(chan string),
        false,
    }

    go s.dispatch()

    return s
}

func (s *Server) ServeHTTP(response http.ResponseWriter, request *http.Request) {
    flusher, ok := response.(http.Flusher)

    if !ok {
        http.Error(response, "Streaming unsupported.", http.StatusInternalServerError)
        return
    }

    h := response.Header()

    if s.options.HasHeaders() {
        for k, v := range s.options.Headers {
            h.Set(k, v)
        }
    }

    if request.Method == "GET" {
        h.Set("Content-Type", "text/event-stream")
        h.Set("Cache-Control", "no-cache")
        h.Set("Connection", "keep-alive")

        var channelName string

        if s.options.ChannelNameFunc == nil {
            channelName = request.URL.Path
        } else {
            channelName = s.options.ChannelNameFunc(request)
        }

        lastEventId := request.Header.Get("Last-Event-ID")
        c := NewClient(lastEventId, channelName)
        s.addClient <- c
        closeNotify := response.(http.CloseNotifier).CloseNotify()

        go func() {
            <-closeNotify
            s.removeClient <- c
        }()

        for msg := range c.send {
            msg.retry = s.options.RetryInterval
            fmt.Fprintf(response, msg.String())
            flusher.Flush()
        }
    } else if request.Method != "OPTIONS" {
        response.WriteHeader(http.StatusMethodNotAllowed)
    }
}

// SendMessage broadcast a message to all clients in a channel.
// If channel is an empty string, it will broadcast the message to all channels.
func (s *Server) SendMessage(channel string, message *Message) {
    if len(channel) == 0 {
        if s.Debug {
            log.Printf("go-sse: broadcasting message to all channels.")
        }

        for _, ch := range s.channels {
            ch.SendMessage(message)
        }
    } else if ch, ok := s.channels[channel]; ok {
        if s.Debug {
            log.Printf("go-sse: message sent to channel '%s'.", ch.name)
        }
        ch.SendMessage(message)
    } else if s.Debug {
        log.Printf("go-sse: message not sent because channel '%s' has no clients.", channel)
    }
}

// Restart closes all channels and clients and allow new connections.
func (s *Server) Restart() {
    if s.Debug {
        log.Printf("go-sse: restarting server.")
    }
    
    s.close()
}

// Shutdown performs a graceful server shutdown.
func (s *Server) Shutdown() {
    s.shutdown <- true
}

func (s *Server) ClientCount() int {
    i := 0

    for _, channel := range s.channels {
        i += channel.ClientCount()
    }

    return i
}

func (s *Server) HasChannel(name string) bool {
    _, ok := s.channels[name]
    return ok
}

func (s *Server) GetChannel(name string) (*Channel, bool)  {
    ch, ok := s.channels[name]
    return ch, ok
}

func (s *Server) Channels() []string  {
    channels := []string{}

    for name, _ := range s.channels {
        channels = append(channels, name)
    }

    return channels
}

func (s *Server) CloseChannel(name string) {
    s.closeChannel <- name
}

func (s *Server) close() {
    for name, _ := range s.channels {
        s.closeChannel <- name
    }
}

func (s *Server) dispatch() {
    if s.Debug {
        log.Printf("go-sse: server started.")
    }
    
    for {
        select {

        // New client connected.
        case c := <- s.addClient:
            ch, exists := s.channels[c.channel]

            if !exists {
                ch = NewChannel(c.channel)
                s.channels[ch.name] = ch
                
                if s.Debug {
                    log.Printf("go-sse: channel '%s' created.", ch.name)
                }
            }

            ch.addClient(c)
            if s.Debug {
                log.Printf("go-sse: new client connected to channel '%s'.", ch.name)
            }

            if s.options.ClientConnected != nil {
                s.options.ClientConnected(c)
            }

        // Client disconnected.
        case c := <- s.removeClient:
            if ch, exists := s.channels[c.channel]; exists {
                if s.options.ClientDisconnected != nil {
                    s.options.ClientDisconnected(c)
                }

                ch.removeClient(c)
                if s.Debug {
                    log.Printf("go-sse: client disconnected from channel '%s'.", ch.name)
                }

                if s.Debug {
                    log.Printf("go-sse: checking if channel '%s' has clients.", ch.name)
                }

                if ch.ClientCount() == 0 {
                    delete(s.channels, ch.name)
                    ch.Close()
                    
                    if s.Debug {
                        log.Printf("go-sse: channel '%s' has no clients.", ch.name)
                    }
                }
            }

        // Close channel and all clients in it.
        case channel := <- s.closeChannel:
            if ch, exists := s.channels[channel]; exists {
                delete(s.channels, channel)
                ch.Close()
                
                if s.Debug {
                    log.Printf("go-sse: channel '%s' closed.", ch.name)
                }
            } else if s.Debug {
                log.Printf("go-sse: requested to close channel '%s', but it doesn't exists.", channel)
            }

        // Event Source shutdown.
        case <- s.shutdown:
            s.close()
            close(s.addClient)
            close(s.removeClient)
            close(s.closeChannel)
            close(s.shutdown)
            
            if s.Debug {
                log.Printf("go-sse: server stopped.")
            }
            return
        }
    }
}
