package main

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path"

	"github.com/cosmos/cosmos-sdk/baseapp"
	"github.com/cosmos/cosmos-sdk/x/ibc"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	abci "github.com/tendermint/tendermint/abci/types"
	"github.com/tendermint/tendermint/crypto/ed25519"

	cmn "github.com/tendermint/tendermint/libs/common"
	dbm "github.com/tendermint/tendermint/libs/db"
	"github.com/tendermint/tendermint/libs/log"

	bam "github.com/cosmos/cosmos-sdk/baseapp"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/cosmos-sdk/x/auth"
	"github.com/cosmos/cosmos-sdk/x/bank"
	"github.com/cosmos/cosmos-sdk/x/params"
	"github.com/cosmos/cosmos-sdk/x/sidechain"
	"github.com/cosmos/cosmos-sdk/x/slashing"
	"github.com/cosmos/cosmos-sdk/x/stake"

	gaia "github.com/cosmos/cosmos-sdk/cmd/gaia/app"
)

func runHackCmd(cmd *cobra.Command, args []string) error {

	if len(args) != 1 {
		return fmt.Errorf("Expected 1 arg")
	}

	// ".gaiad"
	dataDir := args[0]
	dataDir = path.Join(dataDir, "data")

	// load the app
	logger := log.NewTMLogger(log.NewSyncWriter(os.Stdout))
	db, err := dbm.NewGoLevelDB("gaia", dataDir)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	app := NewGaiaApp(logger, db, baseapp.SetPruning(viper.GetString("pruning")))

	// print some info
	id := app.LastCommitID()
	lastBlockHeight := app.LastBlockHeight()
	fmt.Println("ID", id)
	fmt.Println("LastBlockHeight", lastBlockHeight)

	//----------------------------------------------------
	// XXX: start hacking!
	//----------------------------------------------------
	// eg. gaia-6001 testnet bug
	// We paniced when iterating through the "bypower" keys.
	// The following powerKey was there, but the corresponding "trouble" validator did not exist.
	// So here we do a binary search on the past states to find when the powerKey first showed up ...

	// operator of the validator the bonds, gets jailed, later unbonds, and then later is still found in the bypower store
	trouble := hexToBytes("D3DC0FF59F7C3B548B7AFA365561B87FD0208AF8")
	// this is his "bypower" key
	powerKey := hexToBytes("05303030303030303030303033FFFFFFFFFFFF4C0C0000FFFED3DC0FF59F7C3B548B7AFA365561B87FD0208AF8")

	topHeight := lastBlockHeight
	bottomHeight := int64(0)
	checkHeight := topHeight
	for {
		// load the given version of the state
		err = app.LoadVersion(checkHeight, app.keyMain)
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		ctx := app.NewContext(sdk.RunTxModeCheck, abci.Header{})

		// check for the powerkey and the validator from the store
		store := ctx.KVStore(app.keyStake)
		res := store.Get(powerKey)
		val, _ := app.stakeKeeper.GetValidator(ctx, trouble)
		fmt.Println("checking height", checkHeight, res, val)
		if res == nil {
			bottomHeight = checkHeight
		} else {
			topHeight = checkHeight
		}
		checkHeight = (topHeight + bottomHeight) / 2
	}
}

func base64ToPub(b64 string) ed25519.PubKeyEd25519 {
	data, _ := base64.StdEncoding.DecodeString(b64)
	var pubKey ed25519.PubKeyEd25519
	copy(pubKey[:], data)
	return pubKey

}

func hexToBytes(h string) []byte {
	trouble, _ := hex.DecodeString(h)
	return trouble

}

//--------------------------------------------------------------------------------
// NOTE: This is all copied from gaia/app/app.go
// so we can access internal fields!

const (
	appName = "GaiaApp"
)

// default home directories for expected binaries
var (
	DefaultCLIHome  = os.ExpandEnv("$HOME/.gaiacli")
	DefaultNodeHome = os.ExpandEnv("$HOME/.gaiad")
)

// Extended ABCI application
type GaiaApp struct {
	*bam.BaseApp
	cdc *codec.Codec

	// keys to access the substores
	keyMain        *sdk.KVStoreKey
	keyAccount     *sdk.KVStoreKey
	keyStake       *sdk.KVStoreKey
	keyStakeReward *sdk.KVStoreKey
	tkeyStake      *sdk.TransientStoreKey
	keySlashing    *sdk.KVStoreKey
	keyParams      *sdk.KVStoreKey
	tkeyParams     *sdk.TransientStoreKey
	keyIbc         *sdk.KVStoreKey
	keySide        *sdk.KVStoreKey

	// Manage getting and setting accounts
	accountKeeper  auth.AccountKeeper
	bankKeeper     bank.Keeper
	stakeKeeper    stake.Keeper
	slashingKeeper slashing.Keeper
	paramsKeeper   params.Keeper
	ibcKeeper      ibc.Keeper
}

func NewGaiaApp(logger log.Logger, db dbm.DB, baseAppOptions ...func(*bam.BaseApp)) *GaiaApp {
	cdc := MakeCodec()

	bApp := bam.NewBaseApp(appName, logger, db, auth.DefaultTxDecoder(cdc), sdk.CollectConfig{}, baseAppOptions...)
	bApp.SetCommitMultiStoreTracer(os.Stdout)

	// create your application object
	var app = &GaiaApp{
		BaseApp:        bApp,
		cdc:            cdc,
		keyMain:        sdk.NewKVStoreKey("main"),
		keyAccount:     sdk.NewKVStoreKey("acc"),
		keyStake:       sdk.NewKVStoreKey("stake"),
		keyStakeReward: sdk.NewKVStoreKey("stake_reward"),
		tkeyStake:      sdk.NewTransientStoreKey("transient_stake"),
		keySlashing:    sdk.NewKVStoreKey("slashing"),
		keyParams:      sdk.NewKVStoreKey("params"),
		tkeyParams:     sdk.NewTransientStoreKey("transient_params"),
		keyIbc:         sdk.NewKVStoreKey("ibc"),
		keySide:        sdk.NewKVStoreKey("sc"),
	}

	// define the accountKeeper
	app.accountKeeper = auth.NewAccountKeeper(
		app.cdc,
		app.keyAccount,        // target store
		auth.ProtoBaseAccount, // prototype
	)

	// add handlers
	app.bankKeeper = bank.NewBaseKeeper(app.accountKeeper)
	app.paramsKeeper = params.NewKeeper(app.cdc, app.keyParams, app.tkeyParams)
	app.ibcKeeper = ibc.NewKeeper(app.keyIbc, app.paramsKeeper.Subspace(ibc.DefaultParamspace), ibc.DefaultCodespace, sidechain.NewKeeper(app.keySide, app.paramsKeeper.Subspace(sidechain.DefaultParamspace), app.cdc))

	app.stakeKeeper = stake.NewKeeper(app.cdc, app.keyStake, app.keyStakeReward, app.tkeyStake, app.bankKeeper, nil, app.paramsKeeper.Subspace(stake.DefaultParamspace), app.RegisterCodespace(stake.DefaultCodespace))
	app.slashingKeeper = slashing.NewKeeper(app.cdc, app.keySlashing, app.stakeKeeper, app.paramsKeeper.Subspace(slashing.DefaultParamspace), app.RegisterCodespace(slashing.DefaultCodespace), app.bankKeeper)

	// register message routes
	app.Router().
		AddRoute("bank", bank.NewHandler(app.bankKeeper)).
		AddRoute("stake", stake.NewStakeHandler(app.stakeKeeper))

	// initialize BaseApp
	app.SetInitChainer(app.initChainer)
	app.SetBeginBlocker(app.BeginBlocker)
	app.SetEndBlocker(app.EndBlocker)
	app.SetAnteHandler(auth.NewAnteHandler(app.accountKeeper))
	app.MountStoresIAVL(app.keyMain, app.keyAccount, app.keyStake, app.keySlashing, app.keyParams)
	app.MountStore(app.tkeyParams, sdk.StoreTypeTransient)
	err := app.LoadLatestVersion(app.keyMain)
	if err != nil {
		cmn.Exit(err.Error())
	}

	app.Seal()

	return app
}

// custom tx codec
func MakeCodec() *codec.Codec {
	var cdc = codec.New()
	bank.RegisterCodec(cdc)
	stake.RegisterCodec(cdc)
	slashing.RegisterCodec(cdc)
	auth.RegisterCodec(cdc)
	sdk.RegisterCodec(cdc)
	codec.RegisterCrypto(cdc)
	cdc.Seal()
	return cdc
}

// application updates every end block
func (app *GaiaApp) BeginBlocker(ctx sdk.Context, req abci.RequestBeginBlock) abci.ResponseBeginBlock {
	tags := slashing.BeginBlocker(ctx, req, app.slashingKeeper)

	return abci.ResponseBeginBlock{
		Events: tags.ToEvents(),
	}
}

// application updates every end block
// nolint: unparam
func (app *GaiaApp) EndBlocker(ctx sdk.Context, req abci.RequestEndBlock) abci.ResponseEndBlock {
	ctx = ctx.WithEventManager(sdk.NewEventManager())
	validatorUpdates, _ := stake.EndBlocker(ctx, app.stakeKeeper)
	ibc.EndBlocker(ctx, app.ibcKeeper)

	return abci.ResponseEndBlock{
		ValidatorUpdates: validatorUpdates,
	}
}

// custom logic for gaia initialization
func (app *GaiaApp) initChainer(ctx sdk.Context, req abci.RequestInitChain) abci.ResponseInitChain {
	stateJSON := req.AppStateBytes
	// TODO is this now the whole genesis file?

	var genesisState gaia.GenesisState
	err := app.cdc.UnmarshalJSON(stateJSON, &genesisState)
	if err != nil {
		panic(err) // TODO https://github.com/cosmos/cosmos-sdk/issues/468 // return sdk.ErrGenesisParse("").TraceCause(err, "")
	}

	// load the accounts
	for _, gacc := range genesisState.Accounts {
		acc := gacc.ToAccount()
		app.accountKeeper.SetAccount(ctx, acc)
	}

	// load the initial stake information
	validators, err := stake.InitGenesis(ctx, app.stakeKeeper, genesisState.StakeData)
	if err != nil {
		panic(err) // TODO https://github.com/cosmos/cosmos-sdk/issues/468 // return sdk.ErrGenesisParse("").TraceCause(err, "")
	}

	slashing.InitGenesis(ctx, app.slashingKeeper, genesisState.SlashingData, genesisState.StakeData)

	return abci.ResponseInitChain{
		Validators: validators,
	}
}
