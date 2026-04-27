package openai

import "encoding/json"

const doneToolName = "Done"

var doneToolSchema = json.RawMessage(`{
  "type":"object",
  "properties":{
    "status":{"type":"string","enum":["done","blocked"]},
    "message":{"type":"string"}
  },
  "required":["status","message"],
  "additionalProperties":false
}`)
