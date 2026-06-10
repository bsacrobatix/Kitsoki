import { createRouter, createWebHashHistory } from "vue-router";
import HomeView from "./views/HomeView.vue";
import RunView from "./views/RunView.vue";
import InteractiveView from "./views/InteractiveView.vue";
import EditorPage from "./views/EditorPage.vue";

const router = createRouter({
  // Hash history: works fine for both live and file:// artifact mode.
  history: createWebHashHistory(),
  routes: [
    // The home screen is the multi-story browser + live-session list. (The old
    // single-session SessionList is subsumed by HomeView's active-sessions
    // section.)
    { path: "/", component: HomeView },
    { path: "/s/:sessionId", component: RunView, props: true },
    { path: "/s/:sessionId/chat", component: InteractiveView, props: true },
    // Story editor: per-story static inspector. Story + room selected via
    // query params: /editor?story=<id|path>&room=<id>[&session=<id>].
    { path: "/editor", component: EditorPage },
  ],
});

export default router;
