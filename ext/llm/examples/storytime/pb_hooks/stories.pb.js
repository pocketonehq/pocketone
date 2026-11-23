/// <reference path="../types.d.ts" />
// A deleted story leaves the stories bucket (storytime-stories), so Inspire me stops counting it as told.
onRecordAfterDeleteSuccess((e) => {
  try {
    $llm.search.remove("storytime-stories", e.record.id)
  } catch (_) {}
  e.next()
}, "stories")
