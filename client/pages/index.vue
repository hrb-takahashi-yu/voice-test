<template>
    <div class="container">
        <div>
            <div class="result">{{res+tmp}}</div>

            <div class="links">
                <a @click="start" class="button--green" :class="on?'on':''">start</a>
                <a @click="stop" class="button--grey" :class="on?'':'off'">stop</a>
            </div>
        </div>
    </div>
</template>

<script lang="ts">
import { Component, Vue } from "vue-property-decorator";

@Component
export default class Index extends Vue {
    connection?: WebSocket;

    on = false;

    res = "";

    tmp = "";

    start() {
        this.on = true;
        if (this.connection) {
            return;
        }
        const handleSuccess = (stream: MediaStream) => {
            console.log(stream);
            const context = new AudioContext();
            const input = context.createMediaStreamSource(stream);
            const processor = context.createScriptProcessor(4096, 1, 1);

            // this.connection = new WebSocket("ws://localhost:3005/speaker");
            this.connection = new WebSocket("ws://localhost:3004/speaker");

            this.connection.onmessage = async event => {
                const text = await event.data.text();
                console.log(text);
                this.tmp = text;
            };

            input.connect(processor);
            processor.connect(context.destination);

            processor.onaudioprocess = e => {
                if (!this.connection) {
                    processor.onaudioprocess = null;
                    return;
                }

                const voice = e.inputBuffer.getChannelData(0);

                // TODO: サンプルレートをどうするか
                // フロントでdownRateする or サーバに送信して指定する
                // console.log("sample rate:" + e.inputBuffer.sampleRate);

                let buf = new ArrayBuffer(voice.length * 2);
                let dv = new DataView(buf);

                this.floatTo16BitPCM(dv, voice);
                this.connection.send(buf);
            };
        };

        navigator.mediaDevices
            .getUserMedia({ audio: true, video: false })
            // .getDisplayMedia({ audio: true, video: true })
            .then(handleSuccess);
    }

    // startPc() {
    //     const context = new AudioContext();
    //     const osc = context.createOscillator();
    //     const dest = context.createMediaStreamDestination();
    //     osc.connect(dest);
    //     const stream = dest.stream;

    //     const input = context.createMediaStreamSource(stream);
    //     const processor = context.createScriptProcessor(4096, 1, 1);

    //     this.connection = new WebSocket("ws://localhost:3004/speaker");

    //     this.connection.onmessage = async event => {
    //         const text = await event.data.text();
    //         console.log(text);
    //     };

    //     input.connect(processor);
    //     processor.connect(context.destination);

    //     processor.onaudioprocess = e => {
    //         if (!this.connection) {
    //             processor.onaudioprocess = null;
    //             return;
    //         }

    //         // const voice = this.downRate(
    //         //     e.inputBuffer.getChannelData(0),
    //         //     e.inputBuffer.sampleRate,
    //         //     8000
    //         // );
    //         const voice = e.inputBuffer.getChannelData(0);

    //         // TODO: サンプルレートをどうするか
    //         // フロントでdownRateする or サーバに送信して指定する
    //         console.log("sample rate:" + e.inputBuffer.sampleRate);

    //         let buf = new ArrayBuffer(voice.length * 2);
    //         let dv = new DataView(buf);

    //         this.floatTo16BitPCM(dv, voice);
    //         this.connection.send(buf);
    //     };
    // }

    floatTo16BitPCM(output: DataView, input: Float32Array) {
        let offset = 0;
        for (let i = 0; i < input.length; i++, offset += 2) {
            let s = Math.max(-1, Math.min(1, input[i]));
            output.setInt16(offset, s < 0 ? s * 0x8000 : s * 0x7fff, true);
        }
    }

    downRate(
        buffer: Float32Array,
        fromRate: number,
        toRate: number
    ): Float32Array {
        const rate = fromRate / toRate;
        const result = new Float32Array(Math.round(buffer.length / rate));
        let offsetResult = 0;
        let offsetBuffer = 0;
        while (offsetResult < result.length) {
            let nextOffsetBuffer = Math.round((offsetResult + 1) * rate);
            let accum = 0;
            let count = 0;
            for (
                var i = offsetBuffer;
                i < nextOffsetBuffer && i < buffer.length;
                i++
            ) {
                accum += buffer[i];
                count++;
            }
            result[offsetResult] = accum / count;
            offsetResult++;
            offsetBuffer = nextOffsetBuffer;
        }
        return result;
    }

    stop() {
        this.on = false;
        if (!this.connection) {
            return;
        }
        this.res = this.res + this.tmp;
        this.tmp = "";
        this.connection.close();
        this.connection = undefined;
    }
}
</script>

<style>
.container {
    margin: 0 auto;
    min-height: 100vh;
    display: flex;
    justify-content: center;
    align-items: center;
    text-align: center;
}

.title {
    font-family: "Quicksand", "Source Sans Pro", -apple-system,
        BlinkMacSystemFont, "Segoe UI", Roboto, "Helvetica Neue", Arial,
        sans-serif;
    display: block;
    font-weight: 300;
    font-size: 100px;
    color: #35495e;
    letter-spacing: 1px;
}

.subtitle {
    font-weight: 300;
    font-size: 42px;
    color: #526488;
    word-spacing: 5px;
    padding-bottom: 15px;
}

.links {
    padding-top: 15px;
}

.on {
    color: #fff;
    background-color: #3b8070;
}

.off {
    color: #fff;
    background-color: #35495e;
}
.result {
    white-space: pre-wrap;
}
</style>
