package types

import (
	"errors"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/auth"
	authexported "github.com/cosmos/cosmos-sdk/x/auth/exported"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	vestexported "github.com/cosmos/cosmos-sdk/x/auth/vesting/exported"
	"gopkg.in/yaml.v2"
)

// Assert BaseVestingAccount implements authexported.Account at compile-time
var _ authexported.Account = (*BaseVestingAccount)(nil)

// Assert vesting accounts implement vestexported.VestingAccount at compile-time
var _ vestexported.VestingAccount = (*ContinuousVestingAccount)(nil)
var _ vestexported.VestingAccount = (*PeriodicVestingAccount)(nil)
var _ vestexported.VestingAccount = (*DelayedVestingAccount)(nil)

// Register the vesting account types on the auth module codec
func init() {
	authtypes.RegisterAccountTypeCodec(&BaseVestingAccount{}, "cosmos-sdk/BaseVestingAccount")
	authtypes.RegisterAccountTypeCodec(&ContinuousVestingAccount{}, "cosmos-sdk/ContinuousVestingAccount")
	authtypes.RegisterAccountTypeCodec(&DelayedVestingAccount{}, "cosmos-sdk/DelayedVestingAccount")
	authtypes.RegisterAccountTypeCodec(&PeriodicVestingAccount{}, "cosmos-sdk/PeriodicVestingAccount")
}

// BaseVestingAccount implements the VestingAccount interface. It contains all
// the necessary fields needed for any vesting account implementation.
type BaseVestingAccount struct {
	*auth.BaseAccount

	OriginalVesting  sdk.Coins `json:"original_vesting" yaml:"original_vesting"`   // coins in account upon initialization
	DelegatedFree    sdk.Coins `json:"delegated_free" yaml:"delegated_free"`       // coins that are vested and delegated
	DelegatedVesting sdk.Coins `json:"delegated_vesting" yaml:"delegated_vesting"` // coins that vesting and delegated

	EndTime        int64 `json:"end_time" yaml:"end_time"` // when the coins become unlocked
	StartTime      int64 `json:"start_time,omitempty" yaml:"start_time,omitempty"`
	VestingPeriods VestingPeriods `json:"vesting_periods,omitempty" yaml:"vesting_periods,omitempty"`
}

// NewBaseVestingAccount creates a new BaseVestingAccount object
func NewBaseVestingAccount(baseAccount *auth.BaseAccount, originalVesting sdk.Coins,
	endTime int64) *BaseVestingAccount {

	return &BaseVestingAccount{
		BaseAccount:      baseAccount,
		OriginalVesting:  originalVesting,
		DelegatedFree:    sdk.NewCoins(),
		DelegatedVesting: sdk.NewCoins(),
		EndTime:          endTime,
	}
}

// spendableCoins returns all the spendable coins for a vesting account given a
// set of vesting coins.
//
// CONTRACT: The account's coins, delegated vesting coins, vestingCoins must be
// sorted.
func (bva BaseVestingAccount) spendableCoins(vestingCoins sdk.Coins) sdk.Coins {
	var spendableCoins sdk.Coins
	bc := bva.GetCoins()

	for _, coin := range bc {
		baseAmt := coin.Amount
		vestingAmt := vestingCoins.AmountOf(coin.Denom)
		delVestingAmt := bva.DelegatedVesting.AmountOf(coin.Denom)

		// compute min((BC + DV) - V, BC) per the specification
		min := sdk.MinInt(baseAmt.Add(delVestingAmt).Sub(vestingAmt), baseAmt)
		spendableCoin := sdk.NewCoin(coin.Denom, min)

		if !spendableCoin.IsZero() {
			spendableCoins = spendableCoins.Add(sdk.Coins{spendableCoin})
		}
	}

	return spendableCoins
}

// trackDelegation tracks a delegation amount for any given vesting account type
// given the amount of coins currently vesting.
//
// CONTRACT: The account's coins, delegation coins, vesting coins, and delegated
// vesting coins must be sorted.
func (bva *BaseVestingAccount) trackDelegation(vestingCoins, amount sdk.Coins) {
	bc := bva.GetCoins()

	for _, coin := range amount {
		baseAmt := bc.AmountOf(coin.Denom)
		vestingAmt := vestingCoins.AmountOf(coin.Denom)
		delVestingAmt := bva.DelegatedVesting.AmountOf(coin.Denom)

		// Panic if the delegation amount is zero or if the base coins does not
		// exceed the desired delegation amount.
		if coin.Amount.IsZero() || baseAmt.LT(coin.Amount) {
			panic("delegation attempt with zero coins or insufficient funds")
		}

		// compute x and y per the specification, where:
		// X := min(max(V - DV, 0), D)
		// Y := D - X
		x := sdk.MinInt(sdk.MaxInt(vestingAmt.Sub(delVestingAmt), sdk.ZeroInt()), coin.Amount)
		y := coin.Amount.Sub(x)

		if !x.IsZero() {
			xCoin := sdk.NewCoin(coin.Denom, x)
			bva.DelegatedVesting = bva.DelegatedVesting.Add(sdk.Coins{xCoin})
		}

		if !y.IsZero() {
			yCoin := sdk.NewCoin(coin.Denom, y)
			bva.DelegatedFree = bva.DelegatedFree.Add(sdk.Coins{yCoin})
		}

		bva.Coins = bva.Coins.Sub(sdk.Coins{coin})
	}
}

// TrackUndelegation tracks an undelegation amount by setting the necessary
// values by which delegated vesting and delegated vesting need to decrease and
// by which amount the base coins need to increase.
//
// NOTE: The undelegation (bond refund) amount may exceed the delegated
// vesting (bond) amount due to the way undelegation truncates the bond refund,
// which can increase the validator's exchange rate (tokens/shares) slightly if
// the undelegated tokens are non-integral.
//
// CONTRACT: The account's coins and undelegation coins must be sorted.
func (bva *BaseVestingAccount) TrackUndelegation(amount sdk.Coins) {
	for _, coin := range amount {
		// panic if the undelegation amount is zero
		if coin.Amount.IsZero() {
			panic("undelegation attempt with zero coins")
		}
		delegatedFree := bva.DelegatedFree.AmountOf(coin.Denom)
		delegatedVesting := bva.DelegatedVesting.AmountOf(coin.Denom)

		// compute x and y per the specification, where:
		// X := min(DF, D)
		// Y := min(DV, D - X)
		x := sdk.MinInt(delegatedFree, coin.Amount)
		y := sdk.MinInt(delegatedVesting, coin.Amount.Sub(x))

		if !x.IsZero() {
			xCoin := sdk.NewCoin(coin.Denom, x)
			bva.DelegatedFree = bva.DelegatedFree.Sub(sdk.Coins{xCoin})
		}

		if !y.IsZero() {
			yCoin := sdk.NewCoin(coin.Denom, y)
			bva.DelegatedVesting = bva.DelegatedVesting.Sub(sdk.Coins{yCoin})
		}

		bva.Coins = bva.Coins.Add(sdk.Coins{coin})
	}
}

// GetOriginalVesting returns a vesting account's original vesting amount
func (bva BaseVestingAccount) GetOriginalVesting() sdk.Coins {
	return bva.OriginalVesting
}

// GetDelegatedFree returns a vesting account's delegation amount that is not
// vesting.
func (bva BaseVestingAccount) GetDelegatedFree() sdk.Coins {
	return bva.DelegatedFree
}

// GetDelegatedVesting returns a vesting account's delegation amount that is
// still vesting.
func (bva BaseVestingAccount) GetDelegatedVesting() sdk.Coins {
	return bva.DelegatedVesting
}

// GetEndTime returns a vesting account's end time
func (bva BaseVestingAccount) GetEndTime() int64 {
	return bva.EndTime
}

// GetStartTime returns 0 since base vesting account has no start time
func (bva BaseVestingAccount) GetStartTime() int64 {
	return 0
}

// Validate checks for errors on the account fields
func (bva BaseVestingAccount) Validate() error {
	if (bva.Coins.IsZero() && !bva.OriginalVesting.IsZero()) ||
		bva.OriginalVesting.IsAnyGT(bva.Coins) {
		return errors.New("vesting amount cannot be greater than total amount")
	}
	if !(bva.DelegatedVesting.IsAllLTE(bva.OriginalVesting)) {
		return errors.New("delegated vesting amount cannot be greater than original vesting amount")
	}
	return bva.BaseAccount.Validate()
}

// MarshalYAML returns the YAML representation of an account.
func (bva BaseVestingAccount) MarshalYAML() (interface{}, error) {
	var bs []byte
	var err error
	var pubkey string

	if bva.PubKey != nil {
		pubkey, err = sdk.Bech32ifyAccPub(bva.PubKey)
		if err != nil {
			return nil, err
		}
	}

	bs, err = yaml.Marshal(struct {
		Address          sdk.AccAddress
		Coins            sdk.Coins
		PubKey           string
		AccountNumber    uint64
		Sequence         uint64
		OriginalVesting  sdk.Coins
		DelegatedFree    sdk.Coins
		DelegatedVesting sdk.Coins
		EndTime          int64
		StartTime        int64
		VestingPeriods   VestingPeriods
	}{
		Address:          bva.Address,
		Coins:            bva.Coins,
		PubKey:           pubkey,
		AccountNumber:    bva.AccountNumber,
		Sequence:         bva.Sequence,
		OriginalVesting:  bva.OriginalVesting,
		DelegatedFree:    bva.DelegatedFree,
		DelegatedVesting: bva.DelegatedVesting,
		EndTime:          bva.EndTime,
		StartTime: bva.StartTime,
		VestingPeriods: bva.VestingPeriods,
	})
	if err != nil {
		return nil, err
	}

	return string(bs), err
}

//-----------------------------------------------------------------------------
// Continuous Vesting Account

var _ vestexported.VestingAccount = (*ContinuousVestingAccount)(nil)
var _ authexported.GenesisAccount = (*ContinuousVestingAccount)(nil)

// ContinuousVestingAccount implements the VestingAccount interface. It
// continuously vests by unlocking coins linearly with respect to time.
type ContinuousVestingAccount struct {
	*BaseVestingAccount

	StartTime int64 `json:"start_time" yaml:"start_time"` // when the coins start to vest
}

// NewContinuousVestingAccountRaw creates a new ContinuousVestingAccount object from BaseVestingAccount
func NewContinuousVestingAccountRaw(bva *BaseVestingAccount, startTime int64) *ContinuousVestingAccount {
	return &ContinuousVestingAccount{
		BaseVestingAccount: bva,
		StartTime:          startTime,
	}
}

// NewContinuousVestingAccount returns a new ContinuousVestingAccount
func NewContinuousVestingAccount(baseAcc *auth.BaseAccount, startTime, endTime int64) *ContinuousVestingAccount {
	baseVestingAcc := &BaseVestingAccount{
		BaseAccount:     baseAcc,
		OriginalVesting: baseAcc.Coins,
		EndTime:         endTime,
	}

	return &ContinuousVestingAccount{
		StartTime:          startTime,
		BaseVestingAccount: baseVestingAcc,
	}
}

// GetVestedCoins returns the total number of vested coins. If no coins are vested,
// nil is returned.
func (cva ContinuousVestingAccount) GetVestedCoins(blockTime time.Time) sdk.Coins {
	var vestedCoins sdk.Coins

	// We must handle the case where the start time for a vesting account has
	// been set into the future or when the start of the chain is not exactly
	// known.
	if blockTime.Unix() <= cva.StartTime {
		return vestedCoins
	} else if blockTime.Unix() >= cva.EndTime {
		return cva.OriginalVesting
	}

	// calculate the vesting scalar
	x := blockTime.Unix() - cva.StartTime
	y := cva.EndTime - cva.StartTime
	s := sdk.NewDec(x).Quo(sdk.NewDec(y))

	for _, ovc := range cva.OriginalVesting {
		vestedAmt := ovc.Amount.ToDec().Mul(s).RoundInt()
		vestedCoins = append(vestedCoins, sdk.NewCoin(ovc.Denom, vestedAmt))
	}

	return vestedCoins
}

// GetVestingCoins returns the total number of vesting coins. If no coins are
// vesting, nil is returned.
func (cva ContinuousVestingAccount) GetVestingCoins(blockTime time.Time) sdk.Coins {
	return cva.OriginalVesting.Sub(cva.GetVestedCoins(blockTime))
}

// SpendableCoins returns the total number of spendable coins per denom for a
// continuous vesting account.
func (cva ContinuousVestingAccount) SpendableCoins(blockTime time.Time) sdk.Coins {
	return cva.spendableCoins(cva.GetVestingCoins(blockTime))
}

// TrackDelegation tracks a desired delegation amount by setting the appropriate
// values for the amount of delegated vesting, delegated free, and reducing the
// overall amount of base coins.
func (cva *ContinuousVestingAccount) TrackDelegation(blockTime time.Time, amount sdk.Coins) {
	cva.trackDelegation(cva.GetVestingCoins(blockTime), amount)
}

// GetStartTime returns the time when vesting starts for a continuous vesting
// account.
func (cva *ContinuousVestingAccount) GetStartTime() int64 {
	return cva.StartTime
}

// GetEndTime returns the time when vesting ends for a continuous vesting account.
func (cva *ContinuousVestingAccount) GetEndTime() int64 {
	return cva.EndTime
}

// Validate checks for errors on the account fields
func (cva ContinuousVestingAccount) Validate() error {
	if cva.GetStartTime() >= cva.GetEndTime() {
		return errors.New("vesting start-time cannot be before end-time")
	}

	return cva.BaseVestingAccount.Validate()
}

//-----------------------------------------------------------------------------
// Periodic Vesting Account

var _ vestexported.VestingAccount = (*PeriodicVestingAccount)(nil)
var _ authexported.GenesisAccount = (*PeriodicVestingAccount)(nil)

// PeriodicVestingAccount implements the VestingAccount interface. It
// periodically vests by unlocking coins during each specified period
type PeriodicVestingAccount struct {
	*ContinuousVestingAccount
	VestingPeriods VestingPeriods `json:"vesting_periods" yaml:"vesting_periods"` // the vesting schedule
}

// NewPeriodicVestingAccountRaw creates a new PeriodicVestingAccount object from BaseVestingAccount
func NewPeriodicVestingAccountRaw(bva *BaseVestingAccount, startTime int64, periods VestingPeriods) *PeriodicVestingAccount {

	cva := &ContinuousVestingAccount{
		StartTime:          startTime,
		BaseVestingAccount: bva,
	}
	return &PeriodicVestingAccount{
		ContinuousVestingAccount: cva,
		VestingPeriods:           periods,
	}
}

// NewPeriodicVestingAccount returns a new PeriodicVestingAccount
func NewPeriodicVestingAccount(baseAcc *auth.BaseAccount, startTime int64, periods VestingPeriods) *PeriodicVestingAccount {

	endTime := startTime
	for _, p := range periods {
		endTime += p.PeriodLength
	}
	baseVestingAcc := &BaseVestingAccount{
		BaseAccount:     baseAcc,
		OriginalVesting: baseAcc.Coins,
		EndTime:         endTime,
	}

	cva := &ContinuousVestingAccount{
		StartTime:          startTime,
		BaseVestingAccount: baseVestingAcc,
	}

	return &PeriodicVestingAccount{
		ContinuousVestingAccount: cva,
		VestingPeriods:           periods,
	}
}

// GetVestedCoins returns the total number of vested coins. If no coins are vested,
// nil is returned.
func (pva PeriodicVestingAccount) GetVestedCoins(blockTime time.Time) sdk.Coins {
	var vestedCoins sdk.Coins

	// We must handle the case where the start time for a vesting account has
	// been set into the future or when the start of the chain is not exactly
	// known.
	if blockTime.Unix() <= pva.StartTime {
		return vestedCoins
	} else if blockTime.Unix() >= pva.EndTime {
		return pva.OriginalVesting
	}

	// track the start time of the next period
	currentPeriodStartTime := pva.StartTime
	numberPeriods := len(pva.VestingPeriods)
	// for each period, if the period is over, add those coins as vested and check the next period.
	for i := 0; i < numberPeriods; i++ {
		x := blockTime.Unix() - currentPeriodStartTime
		if x >= pva.VestingPeriods[i].PeriodLength {
			vestedCoins = vestedCoins.Add(pva.VestingPeriods[i].VestingAmount)
			// Update the start time of the next period
			currentPeriodStartTime += pva.VestingPeriods[i].PeriodLength
		} else {
			// if the current period isn't vested, break out of the loop
			break
		}
	}
	return vestedCoins
}

// GetVestingCoins returns the total number of vesting coins. If no coins are
// vesting, nil is returned.
func (pva PeriodicVestingAccount) GetVestingCoins(blockTime time.Time) sdk.Coins {
	return pva.OriginalVesting.Sub(pva.GetVestedCoins(blockTime))
}

// SpendableCoins returns the total number of spendable coins per denom for a
// periodic vesting account.
func (pva PeriodicVestingAccount) SpendableCoins(blockTime time.Time) sdk.Coins {
	return pva.spendableCoins(pva.GetVestingCoins(blockTime))
}

// TrackDelegation tracks a desired delegation amount by setting the appropriate
// values for the amount of delegated vesting, delegated free, and reducing the
// overall amount of base coins.
func (pva *PeriodicVestingAccount) TrackDelegation(blockTime time.Time, amount sdk.Coins) {
	pva.trackDelegation(pva.GetVestingCoins(blockTime), amount)
}

// GetStartTime returns the time when vesting starts for a periodic vesting
// account.
func (pva *PeriodicVestingAccount) GetStartTime() int64 {
	return pva.StartTime
}

// GetEndTime returns the time when vesting ends for a periodic vesting account.
func (pva *PeriodicVestingAccount) GetEndTime() int64 {
	return pva.EndTime
}

// GetVestingPeriods returns vesting periods associated with periodic vesting account.
func (pva *PeriodicVestingAccount) GetVestingPeriods() VestingPeriods {
	return pva.VestingPeriods
}

// Validate checks for errors on the account fields
func (pva PeriodicVestingAccount) Validate() error {
	if pva.GetStartTime() >= pva.GetEndTime() {
		return errors.New("vesting start-time cannot be before end-time")
	}
	endTime := pva.StartTime
	originalVesting := sdk.NewCoins()
	for _, p := range pva.VestingPeriods {
		endTime += p.PeriodLength
		originalVesting.Add(p.VestingAmount)
	}
	if endTime != pva.EndTime {
		return errors.New("vesting end time does not match length of all vesting periods")
	}
	if !originalVesting.IsEqual(pva.OriginalVesting) {
		return errors.New("original vesting coins does not match the sum of all coins in vesting periods")
	}

	return pva.BaseVestingAccount.Validate()
}

//-----------------------------------------------------------------------------
// Delayed Vesting Account

var _ vestexported.VestingAccount = (*DelayedVestingAccount)(nil)
var _ authexported.GenesisAccount = (*DelayedVestingAccount)(nil)

// DelayedVestingAccount implements the VestingAccount interface. It vests all
// coins after a specific time, but non prior. In other words, it keeps them
// locked until a specified time.
type DelayedVestingAccount struct {
	*BaseVestingAccount
}

// NewDelayedVestingAccountRaw creates a new DelayedVestingAccount object from BaseVestingAccount
func NewDelayedVestingAccountRaw(bva *BaseVestingAccount) *DelayedVestingAccount {
	return &DelayedVestingAccount{
		BaseVestingAccount: bva,
	}
}

// NewDelayedVestingAccount returns a DelayedVestingAccount
func NewDelayedVestingAccount(baseAcc *auth.BaseAccount, endTime int64) *DelayedVestingAccount {
	baseVestingAcc := &BaseVestingAccount{
		BaseAccount:     baseAcc,
		OriginalVesting: baseAcc.Coins,
		EndTime:         endTime,
	}

	return &DelayedVestingAccount{baseVestingAcc}
}

// GetVestedCoins returns the total amount of vested coins for a delayed vesting
// account. All coins are only vested once the schedule has elapsed.
func (dva DelayedVestingAccount) GetVestedCoins(blockTime time.Time) sdk.Coins {
	if blockTime.Unix() >= dva.EndTime {
		return dva.OriginalVesting
	}

	return nil
}

// GetVestingCoins returns the total number of vesting coins for a delayed
// vesting account.
func (dva DelayedVestingAccount) GetVestingCoins(blockTime time.Time) sdk.Coins {
	return dva.OriginalVesting.Sub(dva.GetVestedCoins(blockTime))
}

// SpendableCoins returns the total number of spendable coins for a delayed
// vesting account.
func (dva DelayedVestingAccount) SpendableCoins(blockTime time.Time) sdk.Coins {
	return dva.spendableCoins(dva.GetVestingCoins(blockTime))
}

// TrackDelegation tracks a desired delegation amount by setting the appropriate
// values for the amount of delegated vesting, delegated free, and reducing the
// overall amount of base coins.
func (dva *DelayedVestingAccount) TrackDelegation(blockTime time.Time, amount sdk.Coins) {
	dva.trackDelegation(dva.GetVestingCoins(blockTime), amount)
}

// GetStartTime returns zero since a delayed vesting account has no start time.
func (dva *DelayedVestingAccount) GetStartTime() int64 {
	return 0
}

// GetEndTime returns the time when vesting ends for a delayed vesting account.
func (dva *DelayedVestingAccount) GetEndTime() int64 {
	return dva.EndTime
}

// Validate checks for errors on the account fields
func (dva DelayedVestingAccount) Validate() error {
	return dva.BaseVestingAccount.Validate()
}
