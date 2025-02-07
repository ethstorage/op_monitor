# Intro

monitor list:
- [x] balance of op-batcher/op-proposer/op-challenger
- [x] unsafe height of op-node doesn't halt growing
- [x] unsafe height of op-node doesn't reorg
- [x] safe/finalized height of op-node doesn't halt growing
- [x] safe/finalized height of op-node doesn't reorg
- [ ] op-batcher/op-proposer are sending transactions in time
- [ ] transactions from op-batcher/op-proposer doesn't fail


When alarm is needed, an email is sent.

# Usage 

```bash
$ cat config.json

{
    "batcher": "<batcher_address>",
    "proposer": "<proposer_address>",
    "challenger": "<challenger_address>",
    "l1_rpc": "<l1_rpc>",
    "l2_el_rpc": "<l2_el_rpc>",
    "l2_cl_rpc": "<l2_cl_rpc>",
    "min_balance": <min_balance>,
    "max_l2_unsafe_halt_time": <max_l2_unsafe_halt_time>,
    "max_l2_safe_delay": <max_l2_safe_delay>,
    "max_l2_finalized_delay": <max_l2_finalized_delay>,
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