package main

import (
	"github.com/junglegaming/backend-challenge-go/internal/application"
	"go.uber.org/fx"
)

func main() {
	fx.New(application.Module).Run()
}
