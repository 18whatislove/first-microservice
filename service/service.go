package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"runtime"
	"sync"
	"time"

	. "github.com/18whatislove/first-microservice/pb"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/tap"
)

// тут вы пишете код
// обращаю ваше внимание - в этом задании запрещены глобальные переменные

func StartMyMicroservice(ctx context.Context, addr, acl string) error {
	credentials := map[string][]string{}
	err := json.Unmarshal([]byte(acl), &credentials)
	if err != nil {
		return err
	}

	eventL := NewEventLoop()
	eventL.Start()
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}

	server := grpc.NewServer(grpc.InTapHandle(tapFunc(eventL, credentials))) // grpc.UnaryInterceptor(unaryInterceptor), grpc.StreamInterceptor(streamInterceptor),

	RegisterAdminServer(server, NewAdminService(eventL))
	RegisterBizServer(server, NewBizService())

	go func() {
		// fmt.Println("server started")
		err := server.Serve(listener)
		if err != nil {
			fmt.Println(err)
		}
	}()

	go func() {
		<-ctx.Done()
		eventL.Stop()
		server.GracefulStop()
		// fmt.Println("server graceful stopped")
	}()
	return nil
}

func tapFunc(el *EventLoop, credentials map[string][]string) tap.ServerInHandle {
	return func(ctx context.Context, info *tap.Info) (context.Context, error) {
		md, exists := metadata.FromIncomingContext(ctx)
		if !exists {
			fmt.Println("tap func: cant grab metadata from context")
			return ctx, nil
		}
		e := event{method: info.FullMethodName,
			timestamp: time.Now().Unix()}

		if len(md.Get("consumer")) == 0 {
			return ctx, status.Error(codes.Unauthenticated, "metadata doesn't have authentication information")
		}

		consumer := md.Get("consumer")[0]
		p, _ := peer.FromContext(ctx)
		host := p.Addr.String()
		e.consumer = consumer
		e.host = host

		go func(ctx context.Context, e event) {
			el.Events <- e
		}(ctx, e)

		if access := CheckUserCredentials(credentials, consumer, info.FullMethodName); !access {
			return ctx, status.Errorf(codes.Unauthenticated, "user %s doesn't have credentials for using %s method", consumer, info.FullMethodName)
		}

		return ctx, nil
	}
}

type EventLoop struct {
	Events     chan event
	stopSignal chan struct{}
	// Stats       *pb.Stat
	Subscribers map[string]chan event
	mu          sync.Mutex
}

type event struct {
	timestamp              int64
	method, host, consumer string
}

func NewEventLoop() *EventLoop {
	return &EventLoop{
		Events:     make(chan event),
		stopSignal: make(chan struct{}),
		// Stats: &pb.Stat{ByMethod: make(map[string]uint64, 1), ByConsumer: make(map[string]uint64, 1)},
		Subscribers: make(map[string]chan event, 5),
		mu:          sync.Mutex{}}
}

func (el *EventLoop) Start() {
	go func() {
		// for {
		// 	select {
		// 	case e := <-el.Events:
		// el.mu.Lock()
		// for _, sub := range el.Subscribers {
		// 	go func(sub chan event, e event) {
		// 		sub <- e
		// 	}(sub, e)
		// }
		// el.mu.Unlock()
		// 	case <-el.stopSignal:
		// 		return
		// 	}
		// }
		for e := range el.Events {
			el.mu.Lock()
			for _, sub := range el.Subscribers {
				go func(sub chan event, e event) {
					sub <- e
				}(sub, e)
			}
			el.mu.Unlock()
		}
	}()
}

func (e *EventLoop) Subscribe(consumerName string) chan event {
	ch := make(chan event)
	go func() {
		runtime.Gosched()
		e.mu.Lock()
		e.Subscribers[consumerName] = ch
		e.mu.Unlock()
	}()
	return ch
}

func (e *EventLoop) Unsubscribe(consumerName string) error {
	// e.mu.Lock()
	// if _, exists := e.Subscribers[consumerName]; !exists {
	// 	return fmt.Errorf("consumer '%s' doesn't exist\n", consumerName)
	// }
	// e.mu.Unlock()
	go func() {
		e.mu.Lock()
		// e.Subscribers = append(e.Subscribers[:id], e.Subscribers[id+1:]...)
		delete(e.Subscribers, consumerName)
		e.mu.Unlock()
	}()
	return nil
}

func (e *EventLoop) Stop() {
	// blocking
	// e.stopSignal <- struct{}{}
	close(e.Events)
}

type adminService struct {
	EventLoop *EventLoop
	UnimplementedAdminServer
}

func NewAdminService(el *EventLoop) AdminServer {
	return &adminService{EventLoop: el}
}

func CheckUserCredentials(acl map[string][]string, consumer string, method string) bool {
	// /package.service/method
	// var services = [2]string{"Admin", "Biz"}

	perms, exists := acl[consumer]
	if !exists {
		return false
	}
	for _, perm := range perms {
		if perm == method || perm[len(perm)-1] == '*' {
			return true
		}
	}
	return false
}

func (a *adminService) Logging(nothing *Nothing, stream Admin_LoggingServer) error {
	ctx := stream.Context()
	md, _ := metadata.FromIncomingContext(ctx)
	consumerName := md.Get("consumer")[0]

	ch := a.EventLoop.Subscribe(consumerName)
	defer func() {
		if err := a.EventLoop.Unsubscribe(consumerName); err != nil {
			fmt.Println(err)
		}
	}()
	// var event *pb.Event
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case event := <-ch:
			// fmt.Println(consumerName, ":", event.Method)
			// go func(event *pb.Event) {

			if err := stream.Send(&Event{Method: event.method,
				Consumer:  event.consumer,
				Host:      event.host,
				Timestamp: event.timestamp}); err != nil {
				fmt.Println(err)
				// break loop
			}
		}
	}
	return nil
}

func (a *adminService) Statistics(s *StatInterval, stream Admin_StatisticsServer) error {
	interval := time.Duration(s.IntervalSeconds)

	md, _ := metadata.FromIncomingContext(stream.Context())
	consumer := md.Get("consumer")[0]

	eventCh := a.EventLoop.Subscribe(consumer)

	wg := &sync.WaitGroup{}

	wg.Add(1)
	// collect stat info
	go func(i time.Duration) {
		defer wg.Done()
		defer func() {
			if err := a.EventLoop.Unsubscribe(consumer); err != nil {
				fmt.Println(err)
			}
		}()
		ticker := time.NewTicker(interval * time.Second)
		defer ticker.Stop()
		stat := &Stat{
			ByMethod:   make(map[string]uint64, 5),
			ByConsumer: make(map[string]uint64, 5),
		}
		for {
			select {
			default:
			case event := <-eventCh:
				consumer, method := event.consumer, event.method
				stat.ByConsumer[consumer]++
				stat.ByMethod[method]++
			case <-ticker.C:
				// fmt.Println(consumer)
				err := stream.Send(stat)
				if err != nil {
					fmt.Println("statistics method:", err)
					return
				}
				// reset
				stat = &Stat{
					ByMethod:   make(map[string]uint64, 5),
					ByConsumer: make(map[string]uint64, 5),
				}
			case <-stream.Context().Done():
				return
			}
		}
	}(interval)
	wg.Wait()
	return nil
}

type bizService struct {
	UnimplementedBizServer
}

func NewBizService() BizServer {
	return &bizService{}
}