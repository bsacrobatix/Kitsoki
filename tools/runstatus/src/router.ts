import { createRouter, createWebHashHistory } from "vue-router";
import HomeView from "./views/HomeView.vue";
import RunView from "./views/RunView.vue";
import InteractiveView from "./views/InteractiveView.vue";
import ReviewPage from "./views/ReviewPage.vue";

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
    // /review/:sessionId?video=<handle> — the video feedback surface.
    { path: "/review/:sessionId", component: ReviewPage, props: true },
  ],
});

export default router;
