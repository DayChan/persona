Save a place to the user's saved places, or change the saved place of the same name. Only for a place the user just confirmed in this conversation (their current location while they are there, or a phone_location_search result they picked); the user sees and edits it in Settings › Assistant › Saved Places.

```json
{
  "type": "object",
  "required": ["name", "latitude", "longitude"],
  "properties": {
    "name": {
      "type": "string",
      "description": "What the user calls it, e.g. 家 or 公司 (at most 40 characters)"
    },
    "address": {
      "type": "string",
      "description": "The address, as phone_location_search gave it"
    },
    "latitude": {
      "type": "number"
    },
    "longitude": {
      "type": "number"
    },
    "radius_meters": {
      "type": "integer",
      "description": "The reminder circle, 100–2000; leave it out for 150"
    }
  }
}
```
