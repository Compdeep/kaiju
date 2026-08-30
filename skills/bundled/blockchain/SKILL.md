---
name: blockchain
description: "Read live chain data — latest block, latest transaction, an address, a transaction hash — for Ethereum, Solana and Bitcoin. Use when the question is about what is on a chain right now. Every endpoint here is public and needs no API key."
---

## When to Use

Use when the question is about the state of a public blockchain: the latest block,
the latest transaction, the contents of a block, a specific transaction hash, or an
address balance.

Do NOT use for prices, market capitalisation or trading volume. Those are market
data, not chain data, and none of the endpoints below carry them.

## Planning Guidance

### Call the chain, never the explorer

etherscan.io, explorer.solana.com and their equivalents are applications for people.
They sit behind bot detection that answers an automated fetch with a challenge page —
`HTTP 403` and "Just a moment...", or `HTTP 429` and "Vercel Security Checkpoint" —
and no wait, header or retry gets past it, because it is not a rate limit. Three runs
have now been lost to this.

Every one of those sites is a view onto a node that answers JSON over HTTP with no
key at all. Ask the node. The endpoints below are the whole answer to "how do I get
chain data" — do not search for others, and do not guess a path on a host you have
not read a response from.

### Ethereum

One `web_fetch` step. Endpoint `https://ethereum-rpc.publicnode.com`
(`https://eth.drpc.org` and `https://1rpc.io/eth` also answer; `cloudflare-eth.com`
returns 429 and `eth.llamarpc.com` returns 521 — do not use those two).

Latest block with every transaction in it:

- `url`: `https://ethereum-rpc.publicnode.com`
- `method`: `POST`
- `headers`: `{"Content-Type": "application/json"}`
- `body`: `{"jsonrpc":"2.0","id":1,"method":"eth_getBlockByNumber","params":["latest",true]}`

The result carries `number`, `timestamp` and a `transactions` array. Each entry has
`hash`, `from`, `to`, `value`, `gas` and `gasPrice`. The latest transaction is the
last entry of the array.

Other methods, same shape: `eth_blockNumber` (no params), `eth_getTransactionByHash`
(`["0x…"]`), `eth_getBalance` (`["0x…","latest"]`).

### Solana

Two `web_fetch` steps, the second wired to the first. Endpoint
`https://api.mainnet-beta.solana.com`.

This endpoint returns `HTTP 415` and `Invalid content-type` unless the request says
`Content-Type: application/json`. Set the header on every call.

First step, to find the current slot:

- `url`: `https://api.mainnet-beta.solana.com`
- `method`: `POST`
- `headers`: `{"Content-Type": "application/json"}`
- `body`: `{"jsonrpc":"2.0","id":1,"method":"getSlot","params":[{"commitment":"finalized"}]}`

It returns the slot number as `result`. Second step, same url, method and headers,
with that number written into the body:

- `body`: `{"jsonrpc":"2.0","id":1,"method":"getBlock","params":[SLOT,{"encoding":"json","transactionDetails":"signatures","maxSupportedTransactionVersion":0,"rewards":false}]}`

It returns `blockTime` and a `signatures` array. The latest transaction is the last
signature. Ask for `"transactionDetails":"signatures"` unless the question needs
more — the full form returns several megabytes for one block.

There is no public Solscan endpoint for this. `public-api.solscan.io` is gone and
`pro-api.solscan.io` needs a paid token; both have cost runs their remaining
re-plans. Blockchair has no Solana API. Use the RPC above.

### Bitcoin

Plain GET, no body, no headers.

- `https://mempool.space/api/blocks/tip/height` — the current height, as a bare number.
- `https://mempool.space/api/block/<hash>/txids` — every transaction id in a block.
- `https://blockchain.info/latestblock` — height, hash and time in one call.

### Values are in the chain's smallest unit

This is where a plan goes wrong quietly, because the wrong answer looks reasonable.

- Ethereum `value` is **wei**, hex-encoded. Divide by 10^18 for ETH.
- Solana amounts are **lamports**. Divide by 10^9 for SOL.
- Bitcoin amounts are **satoshis**. Divide by 10^8 for BTC.

A run has already reported `7089197479927386` as 7.089 ETH. It is 0.00709 ETH. The
dollar figure beside it, taken from the same response, said so, and nothing checked
the two against each other.

When a response carries both a native amount and a fiat one, divide, then confirm the
two agree before answering. If they disagree by a factor of a thousand, the
conversion is wrong, not the source.

### What NOT to do

- Do not `web_search` first. The search returns price and news pages for every
  phrasing of this question. The endpoint is above; the first step of the plan is
  the fetch.
- Do not fetch an explorer page as a fallback when an API fails. It will fail too,
  and differently enough to look worth another attempt.
- Do not use `api.etherscan.io` V1. It is deprecated and answers with a notice saying
  so. V2 needs a key.
- Do not plan a `compute` step to parse these responses unless the question needs
  arithmetic across many of them. A later step reads one field of an earlier
  response by referencing it; a whole program to pick a value out of JSON is a
  step that does nothing.
- Do not treat a `synthetic_coinbase` entry, or any row with a null hash, as "the
  latest transaction". It is a reward record. Take the newest row that has a hash.
