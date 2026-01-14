import type { Component } from "solid-js";
import { Chart, Title, Tooltip, Legend, Colors, LineController, CategoryScale, LinearScale, PointElement, LineElement } from 'chart.js';
import { Line } from 'solid-chartjs';
import type { TrendPoint } from "../services/api";

Chart.register(Title, Tooltip, Legend, Colors, LineController, CategoryScale, LinearScale, PointElement, LineElement);

interface Props {
    data: TrendPoint[]; 
    range?: number;
}

const TrendChart: Component<Props> = (props) => {
    const chartData = () => {
        const limit = props.range || 100;
        const showData = props.data.slice(0, limit).reverse();
        
        return {
            labels: showData.map(d => new Date(d.run_date).toLocaleDateString()),
            datasets: [{
                label: 'Average (ns)',
                data: showData.map(d => d.avg_ns),
                borderColor: '#0969da',
                backgroundColor: 'rgba(9, 105, 218, 0.1)',
                borderWidth: 2,
                tension: 0,
                pointRadius: 2
            }]
        };
    };

    const chartOptions = {
        responsive: true,
        maintainAspectRatio: false,
        plugins: { legend: { display: false } },
        scales: {
            y: { beginAtZero: true, grid: { color: '#f3f4f6' } },
            x: { display: false }
        }
    };

    return (
        <div class="relative w-full h-full">
            <Line data={chartData()} options={chartOptions} width={500} height={300} />
        </div>
    );
};

export default TrendChart;
