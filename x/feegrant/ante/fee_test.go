package ante_test

import (
	"testing"

	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/codec"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	"github.com/stretchr/testify/suite"
	"github.com/tendermint/tendermint/crypto"
	tmproto "github.com/tendermint/tendermint/proto/tendermint/types"

	"github.com/cosmos/cosmos-sdk/simapp"
	"github.com/cosmos/cosmos-sdk/simapp/helpers"
	"github.com/cosmos/cosmos-sdk/testutil/testdata"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authante "github.com/cosmos/cosmos-sdk/x/auth/ante"
	authkeeper "github.com/cosmos/cosmos-sdk/x/auth/keeper"
	"github.com/cosmos/cosmos-sdk/x/auth/signing"
	"github.com/cosmos/cosmos-sdk/x/auth/tx"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/cosmos/cosmos-sdk/x/feegrant/ante"
	"github.com/cosmos/cosmos-sdk/x/feegrant/keeper"
	"github.com/cosmos/cosmos-sdk/x/feegrant/types"
)

// newAnteHandler is just like auth.NewAnteHandler, except we use the DeductGrantedFeeDecorator
// in order to allow payment of fees via a grant.
//
// This is used for our full-stack tests
func newAnteHandler(
	ak authkeeper.AccountKeeper, bankKeeper authtypes.BankKeeper,
	dk keeper.Keeper, sigGasConsumer authante.SignatureVerificationGasConsumer,
	signModeHandler signing.SignModeHandler,
) sdk.AnteHandler {
	return sdk.ChainAnteDecorators(
		authante.NewSetUpContextDecorator(), // outermost AnteDecorator. SetUpContext must be called first
		authante.NewMempoolFeeDecorator(),
		authante.NewValidateBasicDecorator(),
		authante.NewValidateMemoDecorator(ak),
		authante.NewConsumeGasForTxSizeDecorator(ak),
		// DeductGrantedFeeDecorator will create an empty account if we sign with no tokens but valid validation
		// This must be before SetPubKey, ValidateSigCount, SigVerification, which error if account doesn't exist yet
		ante.NewDeductGrantedFeeDecorator(ak, bankKeeper, dk),
		authante.NewSetPubKeyDecorator(ak), // SetPubKeyDecorator must be called before all signature verification decorators
		authante.NewValidateSigCountDecorator(ak),
		authante.NewSigGasConsumeDecorator(ak, sigGasConsumer),
		authante.NewSigVerificationDecorator(ak, signModeHandler),
		authante.NewIncrementSequenceDecorator(ak), // innermost AnteDecorator
	)
}

// AnteTestSuite is a test suite to be used with ante handler tests.
type AnteTestSuite struct {
	suite.Suite

	app         *simapp.SimApp
	anteHandler sdk.AnteHandler
	ctx         sdk.Context
	clientCtx   client.Context
	txBuilder   client.TxBuilder
}

// returns context and app with params set on account keeper
// func createTestApp(isCheckTx bool) (*simapp.SimApp, sdk.Context) {
// 	app := simapp.Setup(isCheckTx)
// 	ctx := app.BaseApp.NewContext(isCheckTx, tmproto.Header{})
// 	app.AccountKeeper.SetParams(ctx, authtypes.DefaultParams())

// 	return app, ctx
// }

// SetupTest setups a new test, with new app, context, and anteHandler.
func (suite *AnteTestSuite) SetupTest(isCheckTx bool) {
	suite.app, suite.ctx = createTestApp(isCheckTx)
	suite.ctx = suite.ctx.WithBlockHeight(1)

	// Set up TxConfig.
	encodingConfig := simapp.MakeTestEncodingConfig()
	// We're using TestMsg encoding in some tests, so register it here.
	encodingConfig.Amino.RegisterConcrete(&testdata.TestMsg{}, "testdata.TestMsg", nil)
	testdata.RegisterInterfaces(encodingConfig.InterfaceRegistry)

	suite.clientCtx = client.Context{}.
		WithTxConfig(encodingConfig.TxConfig)

	suite.anteHandler = simapp.NewAnteHandler(suite.app.AccountKeeper, suite.app.BankKeeper, suite.app.FeeGrantKeeper, authante.DefaultSigVerificationGasConsumer, encodingConfig.TxConfig.SignModeHandler())
}

func (suite *AnteTestSuite) TestDeductFeesNoDelegation() {
	suite.SetupTest(true)
	// setup
	app, ctx := suite.app, suite.ctx

	protoTxCfg := tx.NewTxConfig(codec.NewProtoCodec(app.InterfaceRegistry()), tx.DefaultSignModes)

	// this just tests our handler
	dfd := ante.NewDeductGrantedFeeDecorator(app.AccountKeeper, app.BankKeeper, app.FeeGrantKeeper)
	ourAnteHandler := sdk.ChainAnteDecorators(dfd)

	// this tests the whole stack
	anteHandlerStack := suite.anteHandler

	// keys and addresses
	priv1, _, addr1 := testdata.KeyTestPubAddr()
	priv2, _, addr2 := testdata.KeyTestPubAddr()
	priv3, _, addr3 := testdata.KeyTestPubAddr()
	priv4, _, addr4 := testdata.KeyTestPubAddr()

	nonExistedAccNums := make(map[string]uint64)
	nonExistedAccNums[addr1.String()] = 0
	nonExistedAccNums[addr2.String()] = 1
	nonExistedAccNums[addr3.String()] = 2
	nonExistedAccNums[addr4.String()] = 3

	// Set addr1 with insufficient funds
	acc1 := app.AccountKeeper.NewAccountWithAddress(ctx, addr1)
	app.AccountKeeper.SetAccount(ctx, acc1)
	app.BankKeeper.SetBalances(ctx, addr1, []sdk.Coin{sdk.NewCoin("atom", sdk.NewInt(10))})

	// Set addr2 with more funds
	acc2 := app.AccountKeeper.NewAccountWithAddress(ctx, addr2)
	app.AccountKeeper.SetAccount(ctx, acc2)
	app.BankKeeper.SetBalances(ctx, addr2, []sdk.Coin{sdk.NewCoin("atom", sdk.NewInt(99999))})

	// Set grant from addr2 to addr3 (plenty to pay)
	app.FeeGrantKeeper.GrantFeeAllowance(ctx, addr2, addr3, &types.BasicFeeAllowance{
		SpendLimit: sdk.NewCoins(sdk.NewInt64Coin("atom", 500)),
	})

	// Set low grant from addr2 to addr4 (keeper will reject)
	app.FeeGrantKeeper.GrantFeeAllowance(ctx, addr2, addr4, &types.BasicFeeAllowance{
		SpendLimit: sdk.NewCoins(sdk.NewInt64Coin("atom", 20)),
	})

	// Set grant from addr1 to addr4 (cannot cover this )
	app.FeeGrantKeeper.GrantFeeAllowance(ctx, addr2, addr3, &types.BasicFeeAllowance{
		SpendLimit: sdk.NewCoins(sdk.NewInt64Coin("atom", 500)),
	})

	// app.FeeGrantKeeper.GrantFeeAllowance(ctx, addr1, addr4, &types.BasicFeeAllowance{
	// 	SpendLimit: sdk.NewCoins(sdk.NewInt64Coin("atom", 500)),
	// })

	cases := map[string]struct {
		signerKey     cryptotypes.PrivKey
		signer        sdk.AccAddress
		feeAccount    sdk.AccAddress
		feeAccountKey cryptotypes.PrivKey
		handler       sdk.AnteHandler
		fee           int64
		valid         bool
	}{
		"paying with low funds (only ours)": {
			signerKey: priv1,
			signer:    addr1,
			fee:       50,
			handler:   ourAnteHandler,
			valid:     false,
		},
		"paying with good funds (only ours)": {
			signerKey: priv2,
			signer:    addr2,
			fee:       50,
			handler:   ourAnteHandler,
			valid:     true,
		},
		"paying with no account (only ours)": {
			signerKey: priv3,
			signer:    addr3,
			fee:       1,
			handler:   ourAnteHandler,
			valid:     false,
		},
		"no fee with real account (only ours)": {
			signerKey: priv1,
			signer:    addr1,
			fee:       0,
			handler:   ourAnteHandler,
			valid:     true,
		},
		"no fee with no account (only ours)": {
			signerKey: priv4,
			signer:    addr4,
			fee:       0,
			handler:   ourAnteHandler,
			valid:     false,
		},
		"valid fee grant without account (only ours)": {
			signerKey:  priv3,
			signer:     addr3,
			feeAccount: addr2,
			fee:        50,
			handler:    ourAnteHandler,
			valid:      true,
		},
		"no fee grant (only ours)": {
			signerKey:  priv3,
			signer:     addr3,
			feeAccount: addr1,
			fee:        2,
			handler:    ourAnteHandler,
			valid:      false,
		},
		"allowance smaller than requested fee (only ours)": {
			signerKey:  priv4,
			signer:     addr4,
			feeAccount: addr2,
			fee:        50,
			handler:    ourAnteHandler,
			valid:      false,
		},
		"granter cannot cover allowed fee grant (only ours)": {
			signerKey:  priv4,
			signer:     addr4,
			feeAccount: addr1,
			fee:        50,
			handler:    ourAnteHandler,
			valid:      false,
		},
		"paying with low funds (whole stack)": {
			signerKey: priv1,
			signer:    addr1,
			fee:       50,
			handler:   anteHandlerStack,
			valid:     false,
		},
		"paying with good funds (whole stack)": {
			signerKey: priv2,
			signer:    addr2,
			fee:       50,
			handler:   anteHandlerStack,
			valid:     true,
		},
		"paying with no account (whole stack)": {
			signerKey: priv3,
			signer:    addr3,
			fee:       1,
			handler:   anteHandlerStack,
			valid:     false,
		},
		"no fee with real account (whole stack)": {
			signerKey: priv1,
			signer:    addr1,
			fee:       0,
			handler:   anteHandlerStack,
			valid:     true,
		},
		"no fee with no account (whole stack)": {
			signerKey: priv4,
			signer:    addr4,
			fee:       0,
			handler:   anteHandlerStack,
			valid:     false,
		},
		"valid fee grant without account (whole stack)": {
			signerKey:     priv3,
			signer:        addr3,
			feeAccountKey: priv2,
			feeAccount:    addr2,
			fee:           50,
			handler:       anteHandlerStack,
			valid:         true,
		},
		"no fee grant (whole stack)": {
			signerKey:  priv3,
			signer:     addr3,
			feeAccount: addr1,
			fee:        2,
			handler:    anteHandlerStack,
			valid:      false,
		},
		"allowance smaller than requested fee (whole stack)": {
			signerKey:  priv4,
			signer:     addr4,
			feeAccount: addr2,
			fee:        50,
			handler:    anteHandlerStack,
			valid:      false,
		},
		"granter cannot cover allowed fee grant (whole stack)": {
			signerKey:  priv4,
			signer:     addr4,
			feeAccount: addr1,
			fee:        50,
			handler:    anteHandlerStack,
			valid:      false,
		},
	}

	for name, stc := range cases {
		tc := stc // to make scopelint happy
		suite.T().Run(name, func(t *testing.T) {
			fee := sdk.NewCoins(sdk.NewInt64Coin("atom", tc.fee))
			msgs := []sdk.Msg{testdata.NewTestMsg(tc.signer)}

			acc := app.AccountKeeper.GetAccount(ctx, tc.signer)
			privs, accNums, seqs := []cryptotypes.PrivKey{tc.signerKey}, []uint64{nonExistedAccNums[tc.signer.String()]}, []uint64{0}
			if acc != nil {
				privs, accNums, seqs = []cryptotypes.PrivKey{tc.signerKey}, []uint64{acc.GetAccountNumber()}, []uint64{acc.GetSequence()}
			}

			tx, err := helpers.GenTxWithFeePayer(protoTxCfg, msgs, fee, helpers.DefaultGenTxGas, ctx.ChainID(), accNums, seqs, nil, tc.feeAccount, privs...)
			suite.Require().NoError(err)
			_, err = tc.handler(ctx, tx, false)

			if tc.valid {
				suite.Require().NoError(err)
			} else {
				suite.Require().Error(err)
			}
		})
	}
}

// returns context and app with params set on account keeper
func createTestApp(isCheckTx bool) (*simapp.SimApp, sdk.Context) {
	app := simapp.Setup(isCheckTx)
	ctx := app.BaseApp.NewContext(isCheckTx, tmproto.Header{})
	app.AccountKeeper.SetParams(ctx, authtypes.DefaultParams())

	return app, ctx
}

// don't cosume any gas
func SigGasNoConsumer(meter sdk.GasMeter, sig []byte, pubkey crypto.PubKey, params authtypes.Params) error {
	return nil
}

func TestAnteTestSuite(t *testing.T) {
	suite.Run(t, new(AnteTestSuite))
}
