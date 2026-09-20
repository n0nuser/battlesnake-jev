# Benchmark log

What has actually been measured, including the results that did not go the way
we hoped. Every number here came from a run; nothing is projected.

All matches: 11x11 standard rules, run by the official
`BattlesnakeOfficial/rules` CLI against local servers, with `-t 2000` so the
inference round trip is not a handicap. `jev` and `det` are the **same binary**;
`det` runs with `TYPESAFE_API_KEY` unset, so it plays purely deterministically.

Reproduce any of these with `make tournament GAMES=20 MODE=duel LABEL=whatever`.

## The headline so far

| Question | Answer | Evidence |
| --- | --- | --- |
| Does the model beat the same code without it? | Not established | see the duel table below |
| Does it fit a real turn budget? | Yes | 0 turns over 500ms across ~2000 logged turns; max 382ms |
| Can it see the board? | It must | 1-9 without, 5-5 with, same 10 seeds |

## Latency, measured against the live API

| Condition | Latency |
| --- | --- |
| Cold connect, TLS handshake | 640-735 ms |
| Warm, connection reused, 484 input tokens | 253 / 300 / 328 ms (min / p50 / p90) |
| Warm, connection reused, 395 input tokens | 265 / 300 / 383 ms |

Two consequences the design is built around. The pool must stay warm, because a
cold connect costs more than an entire 500ms turn. And payload size barely
moves latency, so keeping the token count down is a cost and answer-quality
lever, not a speed one.

## Showing the model the board

The single largest effect found so far. Before this change the model received
only the scorer's own per-option numbers and a six-field digest, so it was
re-ranking values the code had computed from information the model did not
have. Drawing the board as ASCII costs about forty tokens a call.

Always-ask mode, identical 10 seeds, only the board differs:

| Model sees | Jev | Deterministic |
| --- | --- | --- |
| Labels only | 1 | 9 |
| Labels + board | 5 | 5 |

The override rate was 28% in both, so the model is not overruling the code more
often with the board, it is overruling it better.

## Always-ask is not the way to play

Consulting the model on every turn loses to the plain bot and costs about five
times the tokens. It exists as a measurement mode, not a playing mode.

| Mode | Jev | Deterministic | Games |
| --- | --- | --- | --- |
| Always ask, no board | 1 | 9 | 10 |
| Always ask, with board | 5 | 5 | 10 |

## Duels, selective mode

Seeds 9001-9020, 11x11, `-t 2000`, one Jev snake against one deterministic one.

| Who breaks the close calls | Jev | Det | Draw | Tokens | Cost |
| --- | --- | --- | --- | --- | --- |
| The model | 9 | 10 | 1 | 555,819 | $0.0233 |
| The model, plus advisory nouls | 8 | 10 | 2 | 690,123 | $0.0290 |
| The model, plus negative selection | 6 | 13 | 1 | 631,369 | $0.0265 |
| **A coin** | **2** | **18** | **0** | 0 | $0.0000 |

The control is the interesting row. Reading only the first three, the model
looks like it is contributing nothing: three arms, all indistinguishable from
a coin flip, and the more authority it is given the worse it does.

Then the same decisions handed to an actual coin lose 2-18.

So the model is not choosing at random on these turns. It is finding something
worth roughly seven games in twenty over chance, and the earlier reading - that
it "adds nothing" - was wrong, because it was missing the floor.

What it is not doing is beating the scorer's own tie-break. Handing the same
close calls to the code's fixed direction preference - no API calls at all -
goes 8-10-2, which the model's 9-10-1 does not separate from.

| Who breaks the close calls | Jev | Det | Draw | Calls | Cost |
| --- | --- | --- | --- | --- | --- |
| A coin | 2 | 18 | 0 | 0 | $0.0000 |
| The code's own fixed ordering | 8 | 10 | 2 | 0 | $0.0000 |
| The model | 9 | 10 | 1 | 1021 | $0.0233 |

So the honest reading, at n=20 per arm, is that the model matches a fixed
direction preference and both beat chance by a mile.

That second part is worth stating plainly, because it is the least obvious
thing measured here: in Battlesnake, *committing* to a direction is most of
what breaking a tie is for. A snake that picks randomly between two
equally-scored moves wanders, and wandering fills in its own escape routes. The
scorer's arbitrary-but-consistent preference is already most of the available
value, which leaves the model very little room to add any.

Path counts from the ordering arm, over 2,938 decisions in 20 games, show how
often any of this is even in play:

| Path | Turns | Share |
| --- | --- | --- |
| Options identical in every measured way | 1,591 | 54% |
| Close enough to hand to the tie-breaker | 976 | 33% |
| Only one safe move | 218 | 7% |
| Deterministic scores decisive | 151 | 5% |
| Nothing safe | 2 | <1% |

## The advisory questions do not carry enough signal

Two noul questions were added alongside the move: whether the position is about
to close around us, and whether a rival is manoeuvring to cut us off. The idea
was sound - a one-ply flood fill cannot tell a tight corridor from a closing
trap - but the measurement says the questions as written do not work.

They never fired. Over a full game the probabilities never crossed the 0.65
alarm threshold: `sealed` peaked at 0.42 and `hunted` at 0.56. The 8-10-2 above
therefore tested an inert feature.

Checking the signal directly, over 364 turns with both an advisory answer and
the measured flood-fill space for the move played:

| Signal | Correlation with space | Reading |
| --- | --- | --- |
| `sealed` | -0.278 | Real, in the right direction, but weak |
| `hunted` | -0.085 | No usable signal |

Mean `sealed` was 0.376 in the tightest quarter of positions against 0.312 in
the roomiest: a separation of 0.06 across the whole range of the board. The
model hedges because it is being asked a yes/no about a rare future event, and
because much of what it can see there is what the flood fill already computes.

Next: `hunted` is dropped, and confinement is asked as a Score - a degree along
a described dimension, which is what the primitive is for - and consumed as a
continuous weight rather than a threshold.

## Things that did not work

**A bigger board does not stop the opening collisions.** Four snakes on 19x19,
same seeds as 11x11: first death at turn 12 against turn 8. The opening deaths
come from every snake being the same length and converging on the same food, so
every head-to-head is a mutual kill. It is a standoff, not a crowding problem.

**Penalising ties harder, and ranking fatal squares by how many rivals contest
them, are both principled - and neither shifted the first-death turn beyond
noise at n=6.** Both are kept because the reasoning is sound and the cost is
zero, not because the measurement backed them.

| Seed | Baseline | Harder tie penalty | + contester ranking |
| --- | --- | --- | --- |
| 7001 | 20 | 20 | 20 |
| 7002 | 8 | 8 | 8 |
| 7003 | 66 | 66 | 31 |
| 7004 | 24 | 59 | 24 |
| 7005 | 9 | 9 | 58 |
| 7006 | 8 | 8 | 8 |

## Spend

Input tokens at $0.042 per million; output is free.

| Batch | Tokens | Cost |
| --- | --- | --- |
| Exploration and A/B runs up to 02:30 | ~8.2M | $0.345 |
| Overnight: baseline duels (n=20) | 0.56M | $0.023 |
| Overnight: advisory duels (n=20) | 0.69M | $0.029 |
| Overnight: signal-quality diagnostics | ~0.7M | $0.029 |

Running total for the overnight session is updated as batches complete.
