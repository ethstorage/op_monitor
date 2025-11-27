# Intro

monitor list:
- [x] balance of op-batcher/op-proposer/op-challenger
- [x] unsafe height of op-node doesn't halt growing
- [x] unsafe height of op-node doesn't reorg
- [x] safe/finalized height of op-node doesn't halt growing
- [x] safe/finalized height of op-node doesn't reorg
- [x] L1 fee scalar adjustment alerts (ETH/QKC ratio change & uint32 overflow protection)
- [x] op-proposer/op-challenger are sending transactions in time (via Etherscan API)


If an alarm is triggered, an email is sent immediately. Otherwise, if no alarm occurs within 24 hours, a liveness email is sent.

# Usage 

```bash
$ cat config.json

{
    "batcher": "<batcher_address>",
    "proposer": "<proposer_address>",
    "challenger": "<challenger_address>",
    "batch_inbox": "<inbox_address>",
    "l1_rpc": "<l1_rpc>",
    "l2_el_rpc": "<l2_el_rpc>",
    "l2_cl_rpc": "<l2_cl_rpc>",
    "min_balance": <min_balance>,
    "max_l2_unsafe_halt_time": <max_l2_unsafe_halt_time>,
    "max_l2_safe_delay": <max_l2_safe_delay>,
    "max_l2_finalized_delay": <max_l2_finalized_delay>,
    "last_eth_qkc_ratio": <last_eth_qkc_ratio>,
    "qkc_l1_blob_base_fee_scalar": <qkc_l1_blob_base_fee_scalar>,
    "qkc_l1_base_fee_scalar": <qkc_l1_base_fee_scalar>,
    "l1_blob_base_fee_scalar_multiplier": <l1_blob_base_fee_scalar_multiplier>,
    "l1_base_fee_scalar_multiplier": <l1_base_fee_scalar_multiplier>,
    "ratio_change_threshold": <ratio_change_threshold>,
    "uint32_overflow_threshold": <uint32_overflow_threshold>,
    "etherscan_api_key": "<etherscan_api_key>",
    "max_proposer_tx_interval": <max_proposer_tx_interval>,
    "max_challenger_tx_interval": <max_challenger_tx_interval>,
    "email_config": {
        "server": "<smtp server>",
        "port": <port>,
        "from": "<from_email>",
        "to": [
            "<to_email>"
        ],
        "pass": "<password>"
    }
}

$ go run main.go --config config.json
```