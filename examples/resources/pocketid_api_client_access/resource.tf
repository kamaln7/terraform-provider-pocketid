resource "pocketid_api" "orders" {
  name     = "Orders API"
  resource = "https://api.orders.example.com"

  permissions = {
    "read:orders"  = { name = "Read orders" }
    "write:orders" = { name = "Write orders" }
  }
}

# A confidential client that calls the API as itself, machine to machine. It
# asks Pocket-ID for a token with the client_credentials grant:
#
#   grant_type=client_credentials
#   resource=https://api.orders.example.com
#   scope=read:orders
resource "pocketid_client" "backup" {
  name          = "Backup job"
  callback_urls = ["https://backup.example.com/callback"]
}

resource "pocketid_api_client_access" "backup" {
  api_id    = pocketid_api.orders.id
  client_id = pocketid_client.backup.id

  # Client access (M2M). `client_access` is true when permissions are listed.
  client_permission_ids = [pocketid_api.orders.permissions["read:orders"].id]
}

# A client that calls the API on behalf of a signed-in user, and may also act
# as itself with another permission.
resource "pocketid_client" "web" {
  name          = "Orders web app"
  callback_urls = ["https://orders.example.com/callback"]
}

resource "pocketid_api_client_access" "web" {
  api_id    = pocketid_api.orders.id
  client_id = pocketid_client.web.id

  user_delegated_permission_ids = [
    pocketid_api.orders.permissions["read:orders"].id,
    pocketid_api.orders.permissions["write:orders"].id,
  ]
  client_permission_ids = [pocketid_api.orders.permissions["read:orders"].id]
}
