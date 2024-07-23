package batcher

import (
	"context"
	"math/big"
	"time"

	"github.com/ethereum-optimism/optimism/op-service/eth"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/params"
)

const randomByteCalldataGas = params.TxDataNonZeroGasEIP2028

type (
	ChannelConfigProvider interface {
		ChannelConfig() ChannelConfig
	}

	GasPricer interface {
		SuggestGasPriceCaps(ctx context.Context) (tipCap *big.Int, baseFee *big.Int, blobBaseFee *big.Int, err error)
	}

	DynamicEthChannelConfig struct {
		log       log.Logger
		ctx       context.Context // parent lifecycle context
		timeout   time.Duration   // query timeout
		gasPricer GasPricer

		blobConfig     ChannelConfig
		calldataConfig ChannelConfig
		latestConfig   *ChannelConfig
	}
)

func NewDynamicEthChannelConfig(lgr log.Logger,
	lifeCtx context.Context, reqTimeout time.Duration, gasPricer GasPricer,
	blobConfig ChannelConfig, calldataConfig ChannelConfig,
) *DynamicEthChannelConfig {
	// Copy blobConfig and statically configure fallback calldata config.
	// In the future, we might want to make the calldata config configurable.
	// cdCfg := blobConfig
	// cdCfg.TargetNumFrames = 1
	// cdCfg.MaxFrameSize = 120_000
	// cdCfg.MultiFrameTxs = false

	dec := &DynamicEthChannelConfig{
		log:            lgr,
		ctx:            lifeCtx,
		timeout:        reqTimeout,
		blobConfig:     blobConfig,
		calldataConfig: calldataConfig,
	}
	// start with blob config
	dec.latestConfig = &dec.blobConfig
	return dec
}

func (dec *DynamicEthChannelConfig) ChannelConfig() ChannelConfig {
	ctx, cancel := context.WithTimeout(dec.ctx, dec.timeout)
	defer cancel()
	tipCap, baseFee, blobBaseFee, err := dec.gasPricer.SuggestGasPriceCaps(ctx)
	if err != nil {
		dec.log.Warn("Error querying gas prices, returning latest config", "err", err)
		return *dec.latestConfig
	}

	// We estimate the gas costs of a calldata and blob tx under the assumption that we'd fill
	// a frame fully and compressed random channel data has few zeros, so they can be
	// ignored in the calldata gas price estimation.
	// It is also assumed that a calldata tx would contain exactly one full frame
	// and a blob tx would contain target-num-frames many blobs.

	// It would be nicer to use core.IntrinsicGas, but we don't have the actual data at hand
	calldataBytes := dec.calldataConfig.MaxFrameSize + 1 // + 1 version byte
	calldataGas := big.NewInt(int64(calldataBytes*randomByteCalldataGas + params.TxGas))
	calldataPrice := new(big.Int).Add(baseFee, tipCap)
	calldataCost := new(big.Int).Mul(calldataGas, calldataPrice)

	blobGas := big.NewInt(eth.BlobSize * int64(dec.blobConfig.TargetNumFrames))
	blobCost := new(big.Int).Mul(blobGas, blobBaseFee)
	// blobs still have intrinsic calldata costs
	blobCalldataCost := new(big.Int).Mul(big.NewInt(int64(params.TxGas)), calldataPrice)
	blobCost = blobCost.Add(blobCost, blobCalldataCost)

	blobDataBytes := big.NewInt(eth.MaxBlobDataSize * int64(dec.blobConfig.TargetNumFrames))
	lgr := dec.log.New("base_fee", baseFee, "blob_base_fee", blobBaseFee, "tip_cap", tipCap,
		"calldata_bytes", calldataBytes, "calldata_cost", calldataCost,
		"blob_data_bytes", blobDataBytes, "blob_cost", blobCost)

	// Now we compare the prices normalized to the number of bytes that can be
	// submitted for that price.
	if new(big.Int).Mul(blobCost, big.NewInt(int64(calldataBytes))).
		Cmp(new(big.Int).Mul(calldataCost, blobDataBytes)) == 1 {
		lgr.Info("Using calldata channel config")
		return dec.calldataConfig
	}
	lgr.Info("Using blob channel config")
	return dec.blobConfig
}
