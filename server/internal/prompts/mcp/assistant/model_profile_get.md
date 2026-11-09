Inspect one model profile by id. See its purpose bindings, launcher model fields and named execution tiers (the Agent presets). Add, change or remove a tier with persona_settings key model_profiles.tiers; other fields are edited through the model-profiles HTTP API. Saving does not apply purpose or launcher settings.

```json
{
  "type": "object",
  "properties": {
    "id": {
      "type": "string",
      "description": "Profile id from model_profiles_list"
    }
  },
  "additionalProperties": false,
  "required": [
    "id"
  ]
}
```
