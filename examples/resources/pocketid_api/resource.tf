# An API with two permissions. A client asks for a token for the API with
# resource=https://api.orders.example.com and scope=read:orders.
resource "pocketid_api" "orders" {
  name     = "Orders API"
  resource = "https://api.orders.example.com"

  # Setting permissions replaces all of them. The map key is the permission key,
  # the scope a client requests.
  permissions = {
    "read:orders" = {
      name        = "Read orders"
      description = "Read the orders of the signed-in user"
    }
    "write:orders" = {
      name = "Write orders"
    }
  }
}

# Allow every client registered through a Client ID Metadata Document to use the
# API on behalf of a user, limited to the permissions marked here.
resource "pocketid_api" "catalog" {
  name               = "Catalog API"
  resource           = "https://api.catalog.example.com"
  allow_cimd_clients = true

  permissions = {
    "read:catalog" = {
      name                     = "Read the catalog"
      allowed_for_cimd_clients = true
    }
    "write:catalog" = {
      name = "Write the catalog"
    }
  }
}

# Client access refers to permissions by ID.
output "read_orders_permission_id" {
  value = pocketid_api.orders.permissions["read:orders"].id
}
