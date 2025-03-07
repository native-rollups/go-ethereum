package nativerollup

import (
	"github.com/ethereum/go-ethereum/eth"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/nativerollup/client"
	"github.com/ethereum/go-ethereum/node"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rpc"
)

var caps = []string{
	"execute_registerClientV1",
}

// Register adds the execute API to the full node.
func Register(stack *node.Node, backend *eth.Ethereum) error {
	log.Warn("Execute API enabled", "protocol", "eth")
	stack.RegisterAPIs([]rpc.API{
		{
			Namespace:     "execute",
			Service:       NewExecuteAPI(backend),
			Authenticated: true,
		},
	})
	return nil
}

type ExecuteAPI struct {
	eth *eth.Ethereum

	endpoints []string
}

func NewExecuteAPI(eth *eth.Ethereum) *ExecuteAPI {
	api := &ExecuteAPI{
		eth: eth,
	}
	return api
}

// ExchangeCapabilities returns the current methods provided by this node.
func (api *ExecuteAPI) ExchangeCapabilities([]string) []string {
	return caps
}

func (api *ExecuteAPI) RegisterClientV1(info client.ClientVersionV1) bool {
	params.ExecuteEndpoint = info.Endpoint
	return true
}
