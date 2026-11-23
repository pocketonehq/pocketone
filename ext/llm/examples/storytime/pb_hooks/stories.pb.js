/// <reference path="../types.d.ts" />
// A deleted story leaves the stories bucket (storytime-stories), so Inspire me stops counting it as told. The bucket is
// made first if it isn't there yet (a story deleted before any was told).
onRecordAfterDeleteSuccess((e) => {
  $llm.search.ensure("storytime-stories")
  $llm.search.remove("storytime-stories", e.record.id)
  e.next()
}, "stories")
