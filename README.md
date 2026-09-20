# battlesnake-jev

A [Battlesnake](https://docs.battlesnake.com) written in Go, where the tactics
are deterministic code and the judgment calls go to
[TypeSafe's Jev](https://docs.typesafe.ai) — a System One model that returns
typed answers and probabilities instead of text.

The interesting part of this repository is not that a model plays a game. It is
that the whole thing is measured, including the parts that did not work.
[BENCHMARK.md](BENCHMARK.md) has the numbers.

## The constraint that shaped everything

A Battlesnake has **500ms per turn, and that budget includes the round trip**
from the game engine. Miss it and the engine moves you `up` on turn one, or
repeats your last move after that — usually fatal.

Measured against the live API from Europe:

| Condition | Latency |
| --- | --- |
| Cold connect, TLS handshake | 640–735 ms |
| Warm, connection reused | 253 / 300 / 328 ms (min / p50 / p90) |

A cold connect costs more than an entire turn. So the connection pool is kept
warm and warmed again at startup, the deterministic move is computed **first and
held**, and inference is only consulted when there is budget for it. Across
~2,000 logged turns at the default 500ms budget, **zero turns went over**, and
every deadline miss fell back to the move already in hand.

Payload size barely moves latency, so keeping the token count down is a cost and
answer-quality lever rather than a speed one.

## How a move gets decided

```
/move
 ├─ 1. flood fill, tail reachability, head-to-head       (~0.1ms)
 ├─ 2. apply the cached strategy from the model          (0ms)
 ├─ 3. no safe move        -> least bad, no call
 │     one safe move       -> play it, no call
 │     one clearly best    -> play it, no call
 │     options identical   -> play the first, no call
 │     otherwise, and if the budget covers it:
 │        ask the model, with a hard deadline
 │        └─ miss, error, or an answer outside the safe set
 │           -> the deterministic move, already in hand
 └─ 4. respond, shouting who decided and why

(background, off the critical path)
     posture: survive / eat / hunt / trap
```

Safety is never delegated. An answer naming a move outside the proven-safe set
is discarded, not played.

### What the model is shown

Not the raw request. Identifiers, customisations, shouts and latencies are
dropped in favour of a digest, plus the board drawn as text:

```
...........
.....2.....
.111Bo D333
.....A.....      H my head, # my body, E rival head,
.....0.....      + rival body, o food, x hazard
```

That board costs about forty tokens and it is the single most valuable thing in
the request — see the benchmark.

## Results in brief

Duels against the **same binary** with `TYPESAFE_API_KEY` unset, so the only
difference is whether the model is consulted at all.

| Who breaks the close calls | Games | Result | Share of decisive |
| --- | --- | --- | --- |
| The model | 200 | 101-89-10 | 53.2% |
| Two identical bots — the floor | 60 | 29-23-8 | 55.8% |
| A coin | 20 | 2-18-0 | 10% |

Consulting the model for close calls neither helps nor hurts by any amount two
hundred games can detect: **-2.6 percentage points against the floor, z = -0.34**.
It is also nowhere near random — a coin on the same decisions loses 2-18.

Committing to *a* direction turns out to be most of what breaking a tie is for.
A snake that chooses randomly between two equally-scored moves wanders, and
wandering fills in its own escape routes. The scorer's arbitrary-but-consistent
preference already captures nearly all of that value, which leaves the model
very little room above it.

Across those 200 games the model was called 5,111 times and **missed the turn
deadline once**, covered by the deterministic move already in hand. Cost: $0.12.

Showing the model the board is what separates it from being actively harmful:

| Model sees | Wins | Losses |
| --- | --- | --- |
| The scorer's numbers only | 1 | 9 |
| The scorer's numbers and the board | 5 | 5 |

## Running it

```bash
make tools          # golangci-lint, gofumpt, the Battlesnake rules CLI
make check          # the gate: fmt, vet, lint, test -race, build
make hooks          # install .git/hooks/pre-push -> make check
make run            # serve on :8080

# one local game you can watch in the browser
battlesnake play -W 11 -H 11 -t 2000 \
  --name jev --url http://localhost:8080 \
  --name det --url http://localhost:8081 --browser

make tournament GAMES=20 MODE=duel LABEL=whatever

# render a recorded game as video: the board, plus who decided each move
battlesnake play ... -o game.jsonl          # record the game
LOG_LEVEL=debug make run                    # with the decision log
make replay REC=game.jsonl LOG=server.log OUT=replay VIEW=hero
```

`TYPESAFE_API_KEY` enables inference. Without it the snake plays entirely
deterministically, which is a supported mode and the baseline every benchmark
is measured against.

| Variable | Default | Effect |
| --- | --- | --- |
| `JEV_BOARD_STATE` | on | Draw the board into the state |
| `JEV_TIEBREAK` | on | Consult the model on close calls |
| `JEV_ALWAYS_ASK` | off | Ask on every real choice; for measurement |
| `JEV_ADVISORS` | off | Also ask how confined the position is |
| `JEV_AVOID` | off | Also ask which safe move becomes a trap |
| `JEV_RANDOM_TIEBREAK` | off | Control arm: break ties with a coin |
| `JEV_MARGIN_MS` | 120 | Slack held back from the turn budget |

## Layout

Per the [official Go guidance](https://go.dev/doc/modules/layout) for a server:

```
cmd/battlesnake/     server wiring, configuration, shutdown
internal/api/        wire types and snake customisation
internal/game/       pure board logic, no I/O
internal/strategy/   the decision ladder and per-game state
internal/jev/        TypeSafe client (there is no Go SDK)
internal/server/     the four webhooks
```

`internal/game` is pure functions over a board, which is why it is the
best-tested package here.
