package main

import (
	"context"
	"github.com/cloudwego/kitex/server"
	"log"
	"net"
	"teamie/backend/internal/api"
	"teamie/backend/internal/config"
	"teamie/backend/internal/identity"
	"teamie/backend/kitex_gen/im/identityservice"
)

func main() {
	flags := config.Flags{Path: api.Env("FEATURE_FLAGS_FILE", "")}
	if e := flags.Validate(); e != nil {
		log.Fatal(e)
	}
	dbURL := api.Env("DATABASE_URL", "")
	if dbURL == "" {
		log.Fatal("DATABASE_URL is required")
	}
	origin := api.Env("APP_ORIGIN", "")
	if origin == "" {
		log.Fatal("APP_ORIGIN is required")
	}
	store, e := identity.Open(context.Background(), dbURL, flags, api.Env("REGISTRATION_CODE", ""))
	if e != nil {
		log.Fatal(e)
	}
	defer store.DB.Close()
	addr, e := net.ResolveTCPAddr("tcp", api.Env("RPC_ADDR", ":8881"))
	if e != nil {
		log.Fatal(e)
	}
	rpc := identityservice.NewServer(&identity.RPC{Store: store}, server.WithServiceAddr(addr))
	defer rpc.Stop()
	go func() {
		if e := rpc.Run(); e != nil {
			log.Fatal(e)
		}
	}()
	api.Serve(api.Env("HTTP_ADDR", ":8081"), (&identity.Handler{Store: store, Origin: origin, Secure: api.Env("COOKIE_SECURE", "true") == "true"}).Routes())
}
