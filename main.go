package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/joho/godotenv"

	"talk-to-ugur-back/web"
)

func main() {
	_ = godotenv.Load()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	server, err := web.NewServer(ctx)
	if err != nil {
		panic(err)
	}

	if err = server.Run(ctx); err != nil {
		panic(err)
	}
}
