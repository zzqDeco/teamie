namespace go im

// IM participants remain independent from any future Agent runtime.
struct User {
  1: required string id
  2: required string email
  3: required string displayName
  4: required string kind
  5: required string createdAt
  6: required string platformRole
}

struct PingRequest {}
struct PingResponse { 1: required bool ready }
