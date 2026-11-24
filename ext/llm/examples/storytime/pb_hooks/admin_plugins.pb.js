/// <reference path="../types.d.ts" />
// Story Time in the dashboard's top bar: a superuser UI extension, registered the way PocketHost documents
// (pockethost.io/docs/admin-extensions). PocketBase serves pb_admin_ext/storytime at /_/extensions/storytime/ and loads its
// main.js into the dashboard through /_/extensions.js. Restart after changing this file; main.js changes show on reload.
$app.onServe().bindFunc((e) => {
  e.uiExtensions.push({
    name: "storytime",
    fs: $os.dirFS(`${__hooks}/../pb_admin_ext/storytime`),
  })
  e.next()
})
