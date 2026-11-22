/// <reference path="../types.d.ts" />
// voices: recordings of a real voice for the narrator to imitate (voice cloning). Each is a short clip read from a
// passage, plus the passage itself, which helps the model match the voice. Recorded on Settings → Bedtime Story →
// Narrator and sent with every story's speech call. Superusers only: the stories it reads are public, the raw clip
// isn't.
migrate((app) => {
  const voices = new Collection({
    type: 'base',
    name: 'voices',
    listRule: null,
    viewRule: null,
    createRule: null,
    updateRule: null,
    deleteRule: null,
    fields: [
      { name: 'name', type: 'text', required: true, max: 80 },
      // up to 15 MB: what Vultr takes for a clip
      { name: 'audio', type: 'file', required: true, maxSelect: 1, maxSize: 15728640,
        mimeTypes: ['audio/webm', 'audio/ogg', 'audio/wav', 'audio/x-wav', 'audio/mpeg', 'audio/mp4', 'audio/x-m4a', 'audio/flac'] },
      { name: 'passage', type: 'text', max: 10000 },
      { name: 'created', type: 'autodate', onCreate: true },
    ],
  })
  app.save(voices)
}, (app) => {
  app.delete(app.findCollectionByNameOrId('voices'))
})
