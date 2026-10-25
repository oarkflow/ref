# Browser tests

These drive the real demo page in real Chrome: three users in three separate browser
profiles, with fake cameras and microphones, against a real `rtcd` and a real REF
application. They are how the call features were verified end to end.

```
make test-browser                          # all scenarios
make test-browser SCENARIO=e2ee_group_call # one
```

You need Node 22 or later and Chrome (set `CHROME` if it is not in the usual place).
`make test-browser` builds the two programs first: the application into `.data/refbin`
and the call service, from a checkout of [oarkflow/rtcd](https://github.com/oarkflow/rtcd)
(`RTCD_DIR`, by default next to this repository), into `.data/rtcd`.

| scenario | what it checks |
|---|---|
| `ice_restart_of_a_healthy_connection` | a client restarting ICE when nothing is wrong must not freeze anyone's video |
| `survives_a_network_change` | ICE restart on the same connection, a full rejoin, and the `online` event |
| `e2ee_group_call` | everyone sees everyone, encrypted; the server holds ciphertext; safety codes agree |
| `e2ee_leaver_is_locked_out` | keys rotate when someone leaves |
| `e2ee_refuses_without_encryption` | a browser that cannot encrypt does not join, and nothing from it reaches the server |
| `p2p_call_is_authenticated` | a p2p call with signed DTLS fingerprints and matching safety codes |
| `vp8_simulcast`, `vp9_svc` | three real simulcast layers / one SVC stream, decoded by every viewer |
| `adapts_to_tiles_and_data_saver` | pinning raises a tile's quality, data saver caps the bandwidth, nobody disappears |

Settings: `PORT` (default 18090; `rtcd` uses `PORT+400`), `APPDIR`, `REFBIN`, `RTCDBIN`,
`CHROME`, `CHROME_FLAGS`, `RTCD_LOG` (write the service's output to a file),
`CHROME_LOG` (write Chrome's, with `CHROME_FLAGS="--enable-logging=stderr --v=1"`), and
`AUDIT_DUMP=1` (print what the service refused, from its audit log, after the run).

A scenario that fails prints the service's refusals from its audit log, which usually
says why.

Not here: server-side recording, speaker detection and mixed codecs. Those need plain
(unencrypted) calls and are covered by the SFU's own tests in the rtcd repository, which
use real ffmpeg media.
