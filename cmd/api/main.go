package main

import (
	"go.uber.org/fx"

	"github.com/leonardodacosta/distributedBettingProcessing/internal/composition"
)

func main() {
	fx.New(composition.Module()).Run()
}
