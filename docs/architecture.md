# OpenBooking Architecture

OpenBooking is organized around one authority boundary: the PDS is the write authority
for a single actor DID. Everything else is downstream validation, replay, and
projection.

## Component Boundaries

```mermaid
flowchart LR
    subgraph WritePath[Write path]
        HTTP[HTTP clients]
        INTENT[Intent subject]
        PDS[openbooking-pds]
        PDSDB[(PDS Postgres)]
        BAO[OpenBao Transit]
    end
    subgraph Mesh[Mesh]
        NATS[(NATS JetStream)]
    end
    subgraph Validation[Validation path]
        RELAY[openbooking-relay]
        RELAYDB[(Relay Postgres)]
    end
    subgraph ReadPath[Read path]
        APPVIEW[openbooking-appview]
        APPDB[(AppView Postgres)]
        API[Query clients]
    end

    HTTP --> PDS
    INTENT --> PDS
    PDS --> PDSDB
    PDS --> BAO
    PDS --> NATS
    NATS --> RELAY
    RELAY --> RELAYDB
    RELAY --> NATS
    RELAY --> APPVIEW
    NATS --> APPVIEW
    APPVIEW --> APPDB
    API --> APPVIEW
```

## Runtime Responsibilities

| Component | Responsibility | Authority |
| --- | --- | --- |
| `openbooking-pds` | Validates writes, builds commits, signs state, stores private records, drains outbox | Source of truth for one actor DID |
| `openbooking-relay` | Validates downstream commits, stores replay log, republishes relay envelopes | No write authority over protocol state |
| `openbooking-appview` | Rebuilds read models from relay replay and live events | Read-only projection |
| OpenBao Transit | Signing and encryption operations | Cryptographic helper only |
| NATS JetStream | Durable message transport and replay substrate | Transport, not authority |

## End-To-End Accepted Reservation

```mermaid
sequenceDiagram
    participant Client
    participant PDS as openbooking-pds
    participant Bao as OpenBao Transit
    participant NATS as NATS JetStream
    participant Relay as openbooking-relay
    participant AppView as openbooking-appview

    Client->>PDS: POST /v1/intents
    PDS->>PDS: Load offer and availability
    PDS->>PDS: Build reserve.intent + reserve.commit + availability update
    PDS->>Bao: Sign commit
    Bao-->>PDS: Signature
    PDS->>NATS: Publish OBP.REPO.<providerDid>.COMMIT
    Relay->>NATS: Consume repo commit
    Relay->>Relay: Verify DID key, signature, revision chain
    Relay->>NATS: Publish OBP.RELAY.<relayDid>.COMMIT
    AppView->>Relay: Replay /v1/events?after_sequence=
    AppView->>NATS: Follow OBP.RELAY.<relayDid>.COMMIT
    AppView->>AppView: Project reservation state
    Client->>AppView: GET /v1/providers/{providerDid}/reservations/{intentId}
```

## Data Paths

| Path | Inputs | Outputs |
| --- | --- | --- |
| Write path | PDS HTTP, provider intent subject | Signed repo commits, encrypted private records |
| Validation path | Repo commit subjects | Validated relay envelopes, replay checkpoints |
| Read path | Relay replay API, relay commit subjects | Listings, offers, availability, reservation projections |

## Storage Ownership

| Store | Contents |
| --- | --- |
| PDS Postgres | Repo records, commits, outbox, consumer checkpoints, private records, consent grants |
| Relay Postgres | Validated relay events, actor heads, consumer checkpoints |
| AppView Postgres | Projection tables and replay checkpoints |

## Design Rules

- The PDS is the only component that writes protocol state for its actor DID.
- Relay and AppView can be rebuilt from downstream inputs and checkpoints.
- Private records do not leave the PDS.
- JetStream persistence helps replay and recovery, but signed repo commits remain the
  protocol truth.
