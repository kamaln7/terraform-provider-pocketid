# A client's access to an API is imported with the ID of the API and the ID of
# the client, separated by a `/`. The ID of a client registered through a Client
# ID Metadata Document is a URL: <api_id>/https://client.example.com/metadata.json
terraform import pocketid_api_client_access.backup <api_id>/<client_id>
