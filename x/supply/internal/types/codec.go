package types

import (
	"github.com/cosmos/cosmos-sdk/codec"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/cosmos/cosmos-sdk/x/supply/exported"
)

// RegisterCodec registers the account types and interface
func RegisterCodec(cdc *codec.Codec) {
	cdc.RegisterInterface((*exported.ModuleAccountI)(nil), nil)
	cdc.RegisterInterface((*exported.SupplyI)(nil), nil)
	cdc.RegisterConcrete(&Supply{}, "cosmos-sdk/Supply", nil)
}

// ModuleCdc generic sealed codec to be used throughout module
var ModuleCdc *codec.Codec

func init() {
	cdc := codec.New()
	RegisterCodec(cdc)
	// register new accounts
	authtypes.RegisterAccountTypeCodec(ModuleAccount{}, "cosmos-sdk/ModuleAccount")
	codec.RegisterCrypto(cdc)
	ModuleCdc = cdc.Seal()
}
